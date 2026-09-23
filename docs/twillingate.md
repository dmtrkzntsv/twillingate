# twillingate

What an AI agent — or a person — needs to set up a project, get a site or
app sending data, and answer questions from what comes back. The MCP
endpoint serves this file verbatim as `docs://twillingate`.

Installing twillingate on a server, configuring the collector itself,
standing up Evidence reporting, enabling the API endpoint and replicating
the database are the operator's job, in [deployment.md](deployment.md).

- [What twillingate is](#what-twillingate-is)
- [Set up a project](#set-up-a-project)
- [Instrument a website](#instrument-a-website)
- [The event model](#the-event-model)
- [The wire format](#the-wire-format)
- [Answer questions with the data](#answer-questions-with-the-data)

---

## What twillingate is

One Go binary and one SQLite file. It collects two kinds of analytics
through a single endpoint — views (page views from websites, screen views
from apps and CLIs) and custom product events — rolls them up nightly, and
exposes the result three ways: Evidence dashboards, a read-only SQL
surface, and the API (MCP or HTTP) an AI agent can query in plain language.

It is cookieless by default. An `anonymous` project writes nothing to a
visitor's device unless the tag declares consent, and even then only its
retry queue — never an identifier. Every identifier is salted with a key
that rotates at midnight, so nothing links across days. Raw IP addresses
and User-Agent strings are never stored: the IP becomes a country at
ingest, the User-Agent is checked for crawlers, and both are discarded.

The pieces:

| Command | Does |
| --- | --- |
| `twillingate serve -ingest` | Ingestion: `POST /ingest/events`, the SDK at `/js/twillingate.js`, `/healthz` |
| `twillingate serve -api` | The API endpoint: MCP at `/mcp`, REST at `/api/` |
| `twillingate serve` | Both, on one listener unless `API_ADDR` says otherwise |
| `twillingate dashboards` | Renders the Evidence site from the database |
| `twillingate project`, `key`, `config` | Registry management |
| `twillingate migrate` | Applies schema migrations and exits |

Which of those run, and how they are configured, is set by the operator —
see [Configure the collector](deployment.md#configure-the-collector).

---

## Set up a project

Projects live in the database — a registry table, not a file — and are
managed either through the CLI or, over the API (MCP or HTTP), through the
management tools. Every operation has both forms:

| Operation | CLI | MCP tool |
| --- | --- | --- |
| Create a project | `twillingate project create` | `create_project` |
| Change one | `twillingate project update` | `update_project` |
| List them | `twillingate project list` | `list_projects` |
| Archive / restore | `twillingate project archive` / `restore` | `archive_project` / `restore_project` |
| Issue an ingest key | `twillingate key issue` | `issue_ingest_key` |
| List keys | `twillingate key list` | `list_ingest_keys` |
| Disable / enable a key | `twillingate key disable` / `enable` | `disable_ingest_key` / `enable_ingest_key` |
| Get paste-ready setup | — | `integration_guide` |

`create_project` takes the project fields below as `{name, identity,
allowed_origins, attributes}` (`name` required) and returns the new
`project_id`; `update_project` takes `{project_id, …}` and merges — an
omitted field keeps its value, `allowed_origins: []` clears the list.
`create_project` also takes `skip_key: true` to *not* issue a first key.
`issue_ingest_key` takes `{project_id, label}` and returns the key **and**
a paste-ready snippet. `integration_guide` takes `{project_id, platform}`
where platform is `web`,
`spa`, `server` or `mobile`, and returns the whole setup as markdown with
the project's live key and identity mode already filled in — reach for it
before hand-assembling a snippet.

**Confirm the collector hostname before pasting.** A collector can answer
on several hostnames, and snippets use the default (`PUBLIC_URL`). Ask the
user which one this site should use and change the snippet's `src` if it
differs — the SDK posts to the origin it was loaded from.

There is no rename: the id is the key and the name is free text, so
`project update -name` is a rename. There is no delete over the API
either — deletion needs the CLI.

The CLI forms:

```bash
twillingate project create -name "My App" -identity anonymous \
  -origin https://myapp.com -attr plan -attr tier
twillingate project list                                 # id  identity  name
twillingate project update -id 1 -origin https://myapp.com -origin https://www.myapp.com
twillingate project update -id 1 -clear-origins
twillingate project archive -id 1                        # reversible: `project restore`
twillingate key issue -project-id 1 -label web
twillingate key list -project-id 1
twillingate key disable -project-id 1 -label ios-2025
```

`project update` (and the `update_project` MCP tool) merge rather than
replace: a field you omit keeps its current value. `-origin` and `-attr`
are the exception — supplying either replaces the whole list.
`-clear-origins` (or `allowed_origins: []` over the API) empties the
origins.

| Key | Meaning |
| --- | --- |
| `project_id` | Integer key assigned on create, never reissued after a delete. The `project_id` column on every stored row, the argument of every tool, route and CLI command, and the segment of every dashboard URL. Never transmitted by clients. |
| `name` | Display name. Required; free text, need not be unique; change it with `project update -name`. |
| `identity` | `anonymous` (default) or `identified`. |
| `ingest_keys` | One or more `{key, label, disabled}` credentials. Required. |
| `allowed_origins` | Origins allowed to post for this project. `*` is a wildcard — `https://*.example.com` covers every subdomain, a bare `*` allows any origin. Add `tauri://localhost` or `app://.` for Electron/Tauri. |
| `attributes` | Custom product-event attribute keys to break down. |

### Attribute breakdowns

A project declares which product-event attribute keys are worth reporting on:

```bash
twillingate project update -id 1 -attr plan -attr tier
```

`-attr` is repeatable and, like `-origin`, replaces the whole list when
supplied. Declaring a key drives two things: its own `attr_*` column in
`v_events_flat`, and a value breakdown (counts, unique users and unique
groups per distinct value, per event, per day) in `agg_product_attrs` /
`v_product_attrs`.

Everything sent is still stored regardless. An undeclared key has no
dedicated column but stays reachable via `json_extract(attributes,
'$.junk')`; declaring a key later does not backfill. Rollups run
unconditionally — a project with no `attributes` still gets daily counts and
totals, it just has nothing to break down.

Declaring a key bounds columns, not the values inside one. An
unbounded-cardinality key like a URL or session id would make the aggregate
grow as fast as the raw data it summarises, defeating retention.
`PRODUCT_ATTRIBUTES_TOP_N` (default 50, set
[server-side](deployment.md#configure-the-collector)) guards that globally:
only the top N values per key are kept and the rest collapse into one
`(other)` row whose unique-user and unique-group counts are recomputed from
raw rather than summed. A client sending the literal string `(other)` collides
with that bucket and loses its own count — avoid that value.

`$platform`, `$os` and `$app_version` roll up automatically without being
declared. Do not add them to `attributes`: `$`-prefixed keys are reserved
and never reach the custom attribute blob, so `"attributes": ["$os"]`
extracts nothing.

### Ingest keys

A project needs at least one key, and the key identifies the project — no
payload carries a project field. Multiple keys let a website, an iOS app and
a desktop app be retired on their own schedules.

Keys are **public by design** — they ship in app binaries and page source —
so their job is revocation and project identification, not secrecy. To
retire one: add the replacement, ship clients, watch the old label fall to
zero in the per-minute `ingest summary` log line, then disable it. Deleting
the entry is the eventual cleanup; the flag keeps the step reversible during
a botched rollout.

---

## Instrument a website

The collector serves its own SDK at `/js/twillingate.js`, compiled from the
TypeScript in `sdk/` and embedded in the binary. The served file carries
the collector's origin, so nothing but the key is configured. Load it as a
tag, or inject the same tag from code; bundling the module is not
supported.

### Snippet mode

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…"
        data-identity="anonymous"></script>
```

`twillingate key issue -project-id <id> -label <label>` mints the key and
prints this snippet ready to paste. Its `src` uses `PUBLIC_URL`; a site on
another collector hostname changes the origin, and the served copy posts
back to whichever hostname loaded it.

| Attribute | `init()` option | Meaning |
| --- | --- | --- |
| `data-key` | `key` | The project's ingest key. Required to auto-init; without it the tag loads dormant for `init()` from code. Public by design. |
| `data-identity` | `identity` | `anonymous` (default) or `identified`. Decides what the instance **sends**: anonymous never sends `$user_id`, `$user_name` or `$install_id`, and `identify()` is inert; identified sends them and, with [consent](#consent-and-storage), persists the visitor id, user and group. |
| `data-auto="off"` | `autoPageviews` | Disable automatic pageviews; drive them with `twillingate.page()`. Both default on. |
| `data-mask-url` | `maskUrl` | Rewrite the URL before it is sent. See [Masking](#masking-urls). |
| `data-routing` | `routing` | `history` (default) or `hash`. See [Hash routing](#hash-routing). |
| `data-kind` | `kind` | What this client is: `web` (default), `app`, `cli`, or any short lower-case token. Anything but `web` switches automatic tracking from `$page_view` to `$screen_view` (the route path becomes the screen) and exempts the client from the server's crawler filter. |
| `data-consent` | `consent` | May this instance keep anything on the device. `false` (default): nothing is read from or written to storage. `true`, or the name of a global variable or function a consent manager maintains, unlocks it. See [Consent and storage](#consent-and-storage). |
| `data-instance` | `create(name)` | Register this tag's instance as `twillingate.get(name)` instead of as the default instance. See [Two projects on one page](#two-projects-on-one-page). |

**Every `data-*` attribute has an `init()` equivalent**, enforced by a
test, with one exception: `data-instance` maps to `create()`'s name. The
reverse does not hold: `flushInterval`, `platform`, `appVersion`,
`storage`, `taggedEvents`, `optOut` and `debug` are code-only. Identity is
set from code too, through `identify()`, `group()` and `installId()`,
never pasted into markup.

Views are automatic, including on `history.pushState` and `popstate`, so
single-page apps need no extra code. Elements carrying
`data-twillingate-event` are tracked on click or submit; see [Tagged
elements](#tagged-elements).

Include each tag once. A second copy with the same `data-key`, or with
none, leaves the first in place and logs a warning; a second copy with a
different key replaces the default instance, with a warning. A second
project on the page gives its tag a `data-instance` instead.

**Migrating from Plausible?** The collector also serves
`/js/plausible-shim.js`, an optional second tag that fires events from
Plausible's `plausible-event-*` CSS classes, so a site whose CTAs are
tagged that way keeps working without touching the markup. See
[docs/plausible/](plausible/).

### From code

Load the file without `data-key` and it stays dormant. Or inject the same
tag when the application decides analytics should run:

```js
function loadTwillingate(src) {
  return new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = src;                       // no data-key: loads dormant
    s.onload = () => resolve(window.twillingate);
    s.onerror = () => reject(new Error("twillingate.js failed to load"));
    document.head.appendChild(s);
  });
}
const tg = await loadTwillingate("https://twillingate.example.com/js/twillingate.js");
tg.init({ key: "ak_9f3c…", consent: () => cmp.hasConsent("analytics") });
```

Setting `s.dataset.key` before appending makes the injected script
auto-init on load, as a pasted tag would. Then initialise:

```js
twillingate.init({
  key: "ak_9f3c…",             // required; the collector's origin is in the file
  identity: "anonymous",       // or "identified": decides what is sent
  consent: false,              // default; true, or a global's name, unlocks storage
  storage: "localStorage",     // default; sessionStorage, memory, cookie, or a driver object
  autoPageviews: true,         // default; false for programmatic page() calls
  taggedEvents: true,          // default; false to ignore data-twillingate-event markup
  maskUrl: "uuid",
  routing: "history",
  kind: "app",                 // → $kind ("app" for Electron/Tauri, "cli", …)
  platform: "electron",        // → $platform; defaults to "web" only for kind "web"
  appVersion: "2.4.1",         // → $app_version
  flushInterval: 1000,
  optOut: () => location.hostname === "localhost",   // OR-ed with twillingate_ignore
  debug: false,                // OR-ed with the twillingate_debug flag
});
```

`init()` returns the instance. A second `init()` on a live instance warns
and is ignored.

**Every call is legal before `init()`.** Configuration — `onPage`,
`onEvent`, `attrs`, `identify`, `group`, `installId`, `consent`, `optOut`,
`debug` — takes effect at once; `page`, `screen`, `track` and `flush` are
held in order and run right after `init()`, entry pageview first. That is
what puts the user on the first event of a programmatic instance:

```js
const et = twillingate.create("econumo");   // dormant until init()
et.identify(userHash);                       // known at render time
et.group(workspaceId);
et.init({ key: "ak_econumo…", identity: "identified", autoPageviews: false, taggedEvents: false });
```

Identity set before `init()` stands; a stored value loads only for what
was not set. The hold is capped at 500 calls, oldest dropped.

### Runtime API

```js
twillingate.page();                            // $page_view for the current page
twillingate.page("/settings");                 // $page_view for an explicit path
twillingate.page({ section: "docs" });         // current page with extra attributes
twillingate.onPage((p) => ({ ab: "b" }));      // pageview listener; page(fn) is deprecated
twillingate.onEvent((e) => ({ app: "x" }));    // listener for every event
twillingate.screen("/settings");               // $screen_view (any kind)
twillingate.track("signup", { plan: "pro" });  // opt-in product event
twillingate.attrs({ tier: "beta" });           // default attributes on every event
twillingate.identify("user-123", "Ada");       // $user_id + optional $user_name
twillingate.group("org-9", "Acme Corp");       // $group_id + optional $group_name
twillingate.installId("018f…");                // $install_id for apps; no argument reads it
twillingate.reset();                           // on logout — see below
twillingate.consent(true);                     // pin storage consent; false withdraws, null hands back, none reads
twillingate.optOut(true);                      // write twillingate_ignore; false clears; none reads
twillingate.debug(true);                       // write twillingate_debug; false clears; none reads
twillingate.flush();                           // force-send the queue
twillingate.create("et", { key: "ak_…" });     // a second instance; see below
twillingate.get("et");                         // look it up from anywhere
twillingate.util.maskIds(path);                // helpers, see Masking
```

- `track(name, attrs)` — product event; don't `$`-prefix your names.
- `attrs(attrs)` — default attributes under every event. Successive calls
  merge; `attrs(null)` clears.
- **Precedence** for every event: values the SDK derives (`$host`,
  `$path`, `$referrer`, campaign parameters, display size), then `attrs()`
  defaults, then the call's own attributes, then what listeners return in
  registration order. Later layers win. A `null` or `undefined` value
  drops the key after the last layer, so
  `twillingate.attrs({ $host: "selfhosted_ab12", $referrer: null })` makes
  every pageview carry that host and no referrer. Batch attributes
  (`$os`, `$browser`, …) come from detection and are not changeable.
- `page(arg?, attrs?)` — no argument records the current page; a string
  records that path (with no campaign parameters); an object is extra
  attributes for the current page.
- `onPage(fn)` — a pageview listener; see [Listeners](#listeners).
  `page(fn)` still registers one, with a deprecation warning.
- `onEvent(fn)` — runs for every event after `onPage` has finished with a
  pageview. Receives `{ name, attributes }`; returns attributes to merge,
  `false` to drop the event, anything else to observe. A listener that
  throws drops the event with a warning.
- `consent(granted?)` — see [Consent and storage](#consent-and-storage).
- `identify(user, name?)` — sets `$user_id` and the optional `$user_name`.
  Inert on an anonymous instance, with one warning. Persisted for an
  identified instance with consent. Events already sent stay unattributed.
- `group(id, name?)` — sets `$group_id`/`$group_name` in every mode;
  persisted with consent for an identified instance.
- `installId(id?)` — sets the stable per-install id an app supplies as
  `$install_id`; with no argument returns what would be sent (the declared
  id, else the persisted visitor id, else `null`). Inert on an anonymous
  instance.
- `reset()` — **required on logout.** Clears user, group, names, the
  visitor id and the retry queue.
- `optOut(flag?)` — `true` writes `twillingate_ignore`, `false` clears it;
  the call returns the effective state, the `optOut` callback included.
- `debug(flag?)` — `true` writes `twillingate_debug`, `false` clears it;
  returns the effective state. See [Debugging](#debugging).
- `screen(name, attrs?)` — an explicit `$screen_view`.

### Two projects on one page

`window.twillingate` is the default instance and a factory. `create(name,
opts?)` builds a second instance whose storage keys are prefixed with the
name (`et_visitor`, `et_user`, `et_queue`, …), registers it and, with
options, initialises it; `get(name)` finds it from any later script;
`get()` is the default. A name already in the registry returns the
existing instance with a warning; the name must match
`^[a-z][a-z0-9_]{0,15}$`. No `window.<name>` is ever created.

```js
const et = twillingate.create("et", { key: "ak_app…", identity: "identified" });
et.track("budget_created");
twillingate.get("et").track("export");
```

A tag can do the same: `data-instance="et"` registers its instance as
`twillingate.get("et")`, with or without `data-key`.

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_web…"></script>
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_app…" data-instance="et" data-auto="off"></script>
```

Browser hooks are installed once: one `pushState` patch, one set of
`popstate`, `hashchange`, `online`, `pagehide`, `visibilitychange` and
tagged-element listeners, whatever the number of instances. Each instance
applies its own settings to what it is handed: `autoPageviews` off
ignores navigations, history routing ignores `hashchange`, `taggedEvents`
off ignores markup. `twillingate_ignore` and `twillingate_debug` stay
global and unprefixed: they are about the person and the page, not one
instance.

### Consent and storage

`consent` answers one question: may this instance keep anything on the
device. It defaults to `false`. Without it, records live in memory:
nothing is read from or written to storage apart from the
`twillingate_ignore` and `twillingate_debug` flags the person set. Reading
no consent also deletes anything an earlier session stored under this
instance's keys. A failed batch is retried from memory — when the browser
fires `online`, and once more through `sendBeacon` when the page is hidden
or unloaded — and lost if the tab closes while delivery keeps failing.

With consent, an `identified` instance persists the visitor id, user and
group, and any instance persists the retry queue. An `anonymous` instance
never sends an identifier at all, so there consent only unlocks the queue.

| `data-consent` / `consent` | Meaning |
| --- | --- |
| absent, `""`, `false`, `"false"` | no consent — the default |
| `true`, `"true"` | consent given |
| a function, or a string naming one on `window` | called whenever consent is consulted; the return value is coerced to a boolean |
| a string naming a non-function global | read whenever consent is consulted, so a variable the consent manager flips is picked up live |

A name that resolves to nothing, or a function that throws, fails closed
to no consent with a console warning. The value is consulted at every
storage decision, never cached. When it flips to true, anything waiting in
the memory retry queue is written to storage and an identified instance
starts persisting a visitor id and the user and group it already holds;
when it flips to false, every key this instance owns is deleted.
`consent(true)` / `consent(false)` pins a value over whatever was
declared; `consent(null)` hands control back; `consent()` reads.

**Where keys live** is the `storage` option:

| `storage` | Meaning |
| --- | --- |
| `"localStorage"` (default) | the browser's localStorage |
| `"sessionStorage"` | identity and queue live for the tab |
| `"memory"` | nothing on the device; equivalent to no consent |
| `"cookie"` | one host-only cookie per identity key, `SameSite=Lax`, one year, `Secure` on https; the retry queue stays in memory |
| `{ get(key), set(key, value), remove(key) }` | a custom driver; a driver that throws counts as unavailable |

A cookie on a shared parent domain is a custom driver:

```js
const cookies = {
  get: (k) => document.cookie.match(new RegExp("(?:^|; )" + k + "=([^;]*)"))?.[1] ?? null,
  set: (k, v) => { document.cookie = `${k}=${v}; domain=.example.com; path=/; max-age=31536000; SameSite=Lax; Secure`; },
  remove: (k) => { document.cookie = `${k}=; domain=.example.com; path=/; max-age=0`; },
};
twillingate.init({ key: "ak_…", identity: "identified", consent: true, storage: cookies });
```

### Detection

The SDK detects the operating system, browser and form factor on the
client and sends them on every batch — `$os`, `$browser` and `$device`
always, `$os_version`, `$os_name` and `$browser_version` when it can
determine them. iPadOS in desktop mode and Brave are the two cases a
User-Agent cannot tell apart; Windows 11 and true macOS versions come from
`navigator.userAgentData.getHighEntropyValues`, started at `init()` and
read at flush time. Detection is the only source: there is no override.

```js
twillingate.detectOS();       // { os: "ipados", osVersion: "17.2", osName: "iPadOS 17.2" }
twillingate.detectBrowser();  // { browser: "brave", browserVersion: "126" }
twillingate.detectDevice();   // { device: "tablet" }
```

Each takes an optional `ClientSignals` — the only inputs detection reads
(`userAgent`, `platform`, `maxTouchPoints`, `brave`, `brands`,
`uaPlatform`, `mobile`, `platformVersion`) — and when one is supplied
consults only what it contains. Detection returns `other` for a
User-Agent that names nothing on the list and `unknown` when there is no
User-Agent at all; `watchos`, `visionos` and `wearable` are never
returned. `$platform` is never detected: it is the option, or `web` while
`kind` is `web`, or absent.

### Masking URLs

A pageview carries its location **already split**: `$host` and `$path`,
stored verbatim. `data-mask-url` receives `location.href` and returns a
URL string, which the SDK then splits:

```
https://www.shop.example.com/account/3f8a91c2-…/edit?utm_source=news#top
  → mask  → https://www.shop.example.com/account/[id]/edit
  → split → { $host: "www.shop.example.com", $path: "/account/[id]/edit" }
```

It is resolved inside `init()`, **before the first pageview**, so the entry
page is covered. Three value forms:

| Form | Detection | Behaviour |
| --- | --- | --- |
| Built-ins | every comma-separated token is `uuid`, `numeric` or `hex` | `util.maskIds(href, opts)` |
| Regexp | value starts with `/` | parsed as `/pattern/flags`; matches replaced with `[id]` |
| Function | anything else | `window[value]`, called with the href |

```html
<script>
  function maskPath(href) { return href.replace(/\/orders\/[A-Z]{2}\d+/, "/orders/[ref]"); }
</script>
<script defer … data-mask-url="maskPath"></script>
```

`init({ maskUrl })` accepts all three plus a `RegExp` or a function
directly. **Campaign parameters are read from the original href before
the mask runs.** **Masking fails closed, loudly:** a named function that
does not exist, an unparseable regexp, a mask that throws or returns a
non-string drops pageviews with one `console.warn`.

#### `twillingate.util`

```js
twillingate.util.maskIds(value, opts?)      // uuid + ulid by default; { numeric, hex } opt-in
twillingate.util.withQuery(path, url, keys) // allowlisted query params, sorted
```

`maskIds` works segment by segment and is URL-aware: given an absolute URL
it masks the path and hash segments and never the host. Numeric is opt-in
because `/2024/annual-report` is a real path; `hex` covers 24+ character
hex blobs.

#### Listeners

`onPage(fn)` registers a listener that runs for every pageview, automatic
ones included. It receives `{url, host, path, referrer, attributes}` and
can return an object to merge attributes or `false` to cancel the
pageview. **Values thread through the chain**: each listener receives the
previous one's `host` and `path`.

```js
twillingate.onPage(({ path }) => ({ $path: path.replace(/^\/account\/[^/]+/, "/account/[id]") }));
twillingate.onPage(({ path }) => ({ $path: path.replace(/\/orders\/\d+/, "/orders/[id]") }));
// /account/88/orders/12 → /account/[id]/orders/[id]
```

- `url` is the **post-mask** URL, not `location.href`.
- Returning `false` cancels; later listeners do not run. A listener that
  throws drops the pageview, with a warning.
- A listener that produces no `$path` drops the pageview, with a warning.

The entry pageview is emitted **synchronously** during `init()`. A
listener registered from code before `init()` covers it; one registered
after cannot, and gets a warning. In snippet mode with a key, `init()` runs
when the tag executes, so use `data-mask-url` for anything that must
cover the entry page.

### Tagged elements

Any element with `data-twillingate-event="signup"` tracks that event on
click (main or middle button) or, for a `<form>`, on submit. The nearest
tagged ancestor of the click target wins; `path` is added as an attribute
so a nav CTA is distinguishable by page. Nothing else is read off the
element. The listeners run in the capture phase, so a handler that stops
propagation cannot eat the event. Every instance with `taggedEvents` on
(the default) tracks it; a programmatic instance opts out with
`taggedEvents: false`.

```html
<button data-twillingate-event="signup">Start free trial</button>
<form data-twillingate-event="newsletter">…</form>
```

### Hash routing

`data-routing="hash"` for an app whose routes live in `location.hash`:

```
https://shop.example.com/app/?utm_source=news#/account/3f8a…/edit?tab=billing
  → { $host: "shop.example.com", $path: "/app/#/account/[id]/edit", $utm_source: "news" }
```

`$path` is `pathname + hash`, with the query stripped from both;
`hashchange` emits a pageview in hash mode only; dedup keys on
`pathname + search + hash`. `util.maskIds` masks hash segments too.

### Query-string routing

`pushState` is hooked and the dedup key includes `location.search`, so
query-only navigations fire distinct pageviews. `$path` carries no query
string by default; appending one is an explicit opt-in:

```js
twillingate.onPage(({ url, path }) => ({
  $path: twillingate.util.withQuery(path, url, ["tab", "view"]),
}));
// → /settings?tab=billing
```

`withQuery` appends only allowlisted parameters and sorts them by key.
Allowlist only low-cardinality parameters.

### Transport

Events queue briefly (~1s) and flush as one batch — on the timer, once 20
events accumulate, on `flush()`, and on page unload (`pagehide` /
`visibilitychange` via `sendBeacon`; the key travels in the JSON body
because beacons cannot set headers). Every event carries a UUID and a
client timestamp.

A batch that fails to send (network down, 5xx) is kept for retry — in
memory, or with [consent](#consent-and-storage) in a bounded stored queue
(`twillingate_queue`, or `<instance>_queue`, 50 batches) — and replays
when the browser fires `online`, once more through `sendBeacon` on unload,
and from storage on the next load. Replays dedupe server-side by event id.
A 4xx response drops the batch instead.

### Debugging

`localStorage.twillingate_debug = "true"` in the console, `debug(true)`, or
the `debug` option logs every event and every send of every instance,
prefixed with the instance name, without changing what is sent:

```
[twillingate] $page_view { $host: "shop.example.com", $path: "/account/[id]" }
[twillingate] sent 1 event(s) → 202
[twillingate:et] budget_created { currency: "EUR" }
```

### Privacy behaviour

- Nothing is kept on the device unless the instance declares
  [consent](#consent-and-storage); an anonymous instance never sends an
  identifier even with it.
- Identified instances with consent: a visitor id persists in
  `twillingate_visitor` (or `<instance>_visitor`) together with the user
  and group, in the configured [storage](#consent-and-storage).
- The SDK does not decide where analytics runs: a `localhost` page, a
  `file://` URL and an automated browser are tracked like any other.
  Keep development traffic out with the `optOut` callback:
  `init({ optOut: () => location.hostname === "localhost" })`.
- Opt a device out: `twillingate.optOut(true)`, or
  `localStorage.twillingate_ignore = "true"` by hand.

### Helpers

| URL | What it does |
| --- | --- |
| `/js/plausible-shim.js` | Fires events from Plausible's `plausible-event-*` classes, so a site migrating off Plausible keeps its tagged CTAs without touching the markup. See `docs/plausible/`. |

---

## The event model

Everything goes to one endpoint, `POST /ingest/events`. The event **name**
decides which family it lands in:

| name | family | default `$kind` | feeds |
| --- | --- | --- | --- |
| `$page_view` | views | `web` | the views dashboard, `views_overview`, `views_breakdown`, retention |
| `$screen_view` | views | `app` | same |
| anything else | product | — | `product_events`, `product_attributes` |

The `$` prefix is reserved for the system. An unrecognized `$` **name** is
stored as an ordinary custom event with a warning (forward compatibility);
an unrecognized `$` **attribute key** is dropped, with a warning in the
response body.

### Views (`$page_view`, `$screen_view`)

A view is one page or screen shown to someone. Both names store the same
row; the name only sets the default **kind** — `web` for `$page_view`,
`app` for `$screen_view`. `$kind` overrides it with any token matching
`^[a-z][a-z0-9_]{0,15}$` (`web`, `app`, `cli`, …), usually as a batch
attribute; an invalid value is warned about and the default is used.

**Only `web` has server-side meaning, and only in two ways.** A web view
is enriched from its connection for one thing — the country, from the
IP — and filtered for one thing — a crawler User-Agent is dropped. Every
other kind is never filtered, so a CLI or an Electron app is never dropped
as a crawler whatever HTTP library it uses. Nothing else is derived from
the User-Agent on any kind.

Location arrives already split: `$path` (or its alias `$screen`) is
**required**, `$host` is optional, both stored verbatim; `$path` may
contain a `#` (hash routing) or a `?` (opt-in query routing). Campaign
parameters travel as `$utm_source`, `$utm_medium` and `$utm_campaign`.
`$referrer` is reduced to a source name and, on web views, dropped as a
self-referral when its host matches `$host`.

A client `$session_id` is authoritative; without one, a gap over 30 minutes
per actor starts a new session. A bounce is a single-view session, so
expect high bounce rates on app kinds. Country comes from the connection on
every kind; the IP and the User-Agent header are never stored (the only
User-Agent text that is kept is the `$os_name` a client declares). **A
backend must not relay
web views for other people** — they would all carry the backend's IP (one
country for everyone) and its User-Agent, which the crawler filter drops
when it is curl or a HTTP library.

### Declaring the environment

**The client declares its environment; the server validates and never
parses.** `$platform`, `$os`, `$os_version`, `$os_name`, `$browser`,
`$browser_version`, `$device`, `$device_model`, `$app_version`, `$locale`,
`$display_width` and `$display_height` are stored on any kind exactly as
declared. The JS SDK detects and sends them on every batch (see
[Detection](#detection)); any other client — a backend relay, a native
app, a custom SDK — sends them itself or records `unknown`.

`$platform` is the surface the product is used through and `$os` the
operating system it runs on. They coincide for a native app and diverge
everywhere else: Safari on an iPhone is `platform=web, os=ios`; an
Electron build on a Mac is `platform=electron, os=macos`. `$kind` is
adjacent but coarser and stays as it is.

| Key | Values | Absent | Unrecognised |
| --- | --- | --- | --- |
| `$platform` | Open. Trimmed, lower-cased, then `^[a-z][a-z0-9_]{0,15}$`. Conventionally `web`, `ios`, `android`, `macos`, `windows`, `linux`, `electron`, … | `unknown` | `unknown`, with a warning |
| `$os` | `windows` `macos` `linux` `bsd` `chromeos` `ios` `ipados` `android` `fireos` `harmonyos` `kaios` `tvos` `watchos` `visionos` `tizen` `webos` `playstation` `xbox` `nintendo` `other` `unknown` | `unknown` | `other`, with a warning; the raw value is kept in `os_name` |
| `$browser` | `chrome` `safari` `firefox` `edge` `opera` `samsung_internet` `brave` `vivaldi` `duckduckgo` `yandex` `other` `unknown` | `unknown` | `other`, with a warning |
| `$device` | `desktop` `mobile` `tablet` `wearable` `xr` `other` `unknown` | `unknown` | `other`, with a warning |

Validation of `$os`, `$browser` and `$device` trims, lower-cases and folds
spaces and dashes (`Chrome OS` → `chromeos`, `Samsung Internet` →
`samsung_internet`), so case is forgiven; nothing is ever rejected,
because a client shipping a value
this server has not learned yet must not receive a `4xx`. **`other` and
`unknown` are different answers**: `other` means there is a value and it
is outside the list, `unknown` means no information at all — which is
what makes undeclared traffic visible in a breakdown instead of silently
inflating a real bucket. A client may send either deliberately, and
neither is warned about.

`$os_name` is the OS's full self-reported name with version — `macOS 14.2`,
`Windows 11`, `FreeBSD 14.1` — stored verbatim on views, empty when
absent, never aggregated and never a breakdown dimension. It exists so an
`$os` of `other` stays investigable through `query` while the raw rows
last. `$os_version` and `$browser_version` are free text; the SDK sends
the major browser version and the OS version it can determine. `$device`
is the form factor; consoles and TVs are `other` there because `$os`
already names them. `$device_model` is free text beside it.

Product events keep `$platform` and `$os` as columns and resolve and drop
the rest (`$os_version`, `$os_name`, `$browser`, `$browser_version`,
`$device`), so an SDK that sends every environment key on every batch is
correct and cheap. `$app_version` is the version of whatever client sent
the event, whatever its kind.

### Product (everything else)

Any other name is a custom product event. Don't `$`-prefix your own names.
Attributes are free-form; see [Attribute breakdowns](#attribute-breakdowns)
for which keys get their own column and value breakdown.

### Identity

Each project runs in one of two modes, set server-side:

| Mode | `$user_id`, `$install_id` | `$group_id` | `$user_name` |
| --- | --- | --- | --- |
| `anonymous` (default) | salted hash, salt rotates at 00:00 UTC | stored raw | **ignored** |
| `identified` | stored as given | stored raw | stored |

The actor an event is attributed to resolves as `$user_id` → `$install_id` →
a server-side hash of the connection. In `anonymous` mode the result is
hashed with a daily-rotating salt, so nothing links across days.

How the actor was identified is recorded alongside it (`user`, `install` or
`connection`) and is what retention cohorts on: a connection hash rotates
with the salt and can never appear in a later cohort, so only user- and
install-identified actors are tracked.

Send `$install_id` only if it survives a page load or app restart. An id
minted per load (a client that keeps nothing on the device) makes every load
a new actor that never returns: it inflates active counts and drags
retention toward zero. Leave it out instead — the connection hash then
gives one actor per device per day — and read retention from the `user`
cohort, which follows `$user_id`.

`$group_id` stays raw in both modes: it identifies an organization, not a
natural person, and hashing it would make dashboards unreadable for no real
privacy gain. If your groups are single-person, treat them as personal data.

`$user_name` is ignored entirely in `anonymous` mode. Storing a person's
name against a hash that rotates daily would defeat the anonymisation and
accumulate a fresh row per user per day.

**The server is always the enforcement point.** A client cannot opt a
project into storing raw identifiers.

The JS SDK adds a client-side gate: an instance whose `identity` is
`anonymous` never sends `$user_id`, `$user_name` or `$install_id` at all,
whatever the project's mode.

---

## The wire format

Normative. Three independently written clients (iOS, Android, desktop) must
behave identically from this section alone.

### Endpoint

```
POST /ingest/events
```

The only ingest endpoint. There is no separate pageview, event or batch
path — a single event is a batch of one.

### Authentication

Every project has one or more **ingest keys**. The key identifies the
project, so **no payload carries a project field**.

| Client | How the key travels |
| --- | --- |
| Apps, server-side | `X-Analytics-Key: ak_…` header (preferred) |
| Browsers | `"key"` field in the JSON body |

The header wins when both are present. Browsers must use the body because
`navigator.sendBeacon` cannot set custom headers, and beacons are the only
transport that survives page unload.

An unknown key gets a plain `401` rather than a silent drop: a misconfigured
integration deserves a real error, and the response leaks nothing.

### Envelope

```jsonc
{
  "key": "ak_9f3c…",            // omit when using the header
  "attributes": {                // batch-level defaults, all optional
    "$install_id": "018f1e5a-…",
    "$user_id": "u_123",
    "$user_name": "Ada Lovelace",
    "$group_id": "org_9",
    "$group_name": "Acme Corp",
    "$session_id": "018f1e5b-…",
    "$kind": "app", "$platform": "ios", "$os": "ios",
    "$os_name": "iOS 17.2", "$browser": "safari", "$browser_version": "17",
    "$device": "mobile",
    "$app_version": "2.4.1",
    "$os_version": "17.2",
    "$device_model": "iPhone15,2",
    "$locale": "en-US",
    "$display_width": 1179, "$display_height": 2556
  },
  "events": [
    { "id": "018f1e5c-…", "ts": "2026-08-30T10:00:00Z",
      "name": "$page_view",
      "attributes": { "$host": "shop.example.com", "$path": "/account/[id]/edit",
                      "$utm_source": "newsletter",
                      "$referrer": "https://news.ycombinator.com/" } },
    { "id": "018f1e5d-…", "ts": "2026-08-30T10:00:05Z",
      "name": "$screen_view", "attributes": { "$screen": "/settings" } },
    { "id": "018f1e5e-…", "ts": "2026-08-30T10:00:09Z",
      "name": "subscribed",
      "attributes": { "plan": "pro", "$app_version": "2.5.0" } }
  ]
}
```

An event is `{id, ts, name, attributes}` and nothing else.

### Attribute merge

Batch-level `attributes` are defaults. Per-event `attributes` override them
**key by key**. That is the only merge rule, and it applies to system (`$`)
and ordinary keys alike.

The rule exists for offline queues. A queue flushed after a week may span an
app self-update: per-event override lets a client stamp `$app_version` on
only the events that differ, instead of grouping its queue by context before
flushing. It also lets a client stamp an ordinary attribute
(`"experiment": "variant_b"`) across a whole flush without repeating it.

### Reserved event names

| `name` | Stored as | Default `$kind` | Requires |
| --- | --- | --- | --- |
| `$page_view` | view | `web` | `$path` (or `$screen`) |
| `$screen_view` | view | `app` | `$screen` (or `$path`) |
| anything else | custom event | — | `name` |

An **unrecognized `$` name is stored as an ordinary custom event** with a
warning, never rejected. Clients update on app-store timelines while the
server updates on the operator's, so they will be out of step. Rejecting
would mean a client shipping a future `$session_start` against an older
server receives a `4xx`, which the retry rules classify as a poison batch to
drop — permanent data loss in exactly the window where forward compatibility
matters.

### Reserved attribute keys

| Group | Keys |
| --- | --- |
| Identity | `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` |
| Environment | `$kind` `$platform` `$os` `$os_version` `$os_name` `$browser` `$browser_version` `$device` `$device_model` `$app_version` `$locale` `$display_width` `$display_height` |
| Location | `$host` `$path` `$screen` `$utm_source` `$utm_medium` `$utm_campaign` `$referrer` |

An **unrecognized `$` key is dropped** with a warning. It is not stored as
an ordinary attribute: keeping it would add a column to the flattened event
view for what is almost always a typo.

Location attributes are stored **verbatim**. The server does no URL parsing,
no normalization, no case folding — the client owns normalization. The
client IP and User-Agent are never stored: the IP is enriched into a
country, the User-Agent is read only to drop crawlers, and both are
discarded.

> **Removed in 2.0.** `$url` is no longer a reserved key. A client sending
> it gets a warning that it was ignored *and* a rejection naming `$path`.
> Send `$host` and `$path` instead, with campaign parameters as `$utm_*`.
> The `?ref=` query-parameter referrer fallback is gone with it; set
> `$referrer` explicitly.

### Timestamps and idempotency

**`ts`** is RFC 3339, UTC. Send the client's own clock reading at the moment
the event happened, not at flush time.

The server clamps `ts` to `[received − max_event_age, received + 5 minutes]`
and records the server clock separately. Out-of-range values are **clamped
and counted, never dropped**, so a device with a broken clock still
contributes. `max_event_age` equals the deployment's
`RETENTION_VIEWS_RAW_DAYS` (30 days by default), which guarantees a clamped
event can never target a day that has already been rolled up.

**`id`** is a client-generated UUID (v7 recommended, so ids sort by time).
Supplying one is what makes at-least-once delivery safe: the write ignores a
duplicate primary key, so a batch retried after a timeout that actually
succeeded is a no-op. **Omit `id` and a replayed batch double-counts.**

### Responses and retry

```jsonc
202 {
  "accepted": 2,
  "rejected": 1,
  "errors":   [ { "index": 3, "reason": "view requires $path or $screen" } ],
  "warnings": [ { "index": 1, "reason": "unknown reserved key $app_ver, ignored" } ]
}
```

`errors` and `warnings` are each capped at 10 entries. Rejection is **per
event, not per batch**: one malformed event increments `rejected` and the
rest of the batch still lands, so a single bad event cannot poison a
500-event replay.

| Code | Meaning | Client action |
| --- | --- | --- |
| 202 | Accepted for processing | Do not retry |
| 400 | Malformed envelope | Drop — poison batch |
| 401 | Unknown or disabled key | Drop |
| 403 | `Origin` present and not allowed | Drop |
| 413 | Body or event count over limit | Split and retry |
| 5xx | Server fault | Retry with backoff |

**Normative: retry only on 5xx and network failure. Any other 4xx is a
poison batch — drop it.** Retrying a 400 forever is the most common way a
hand-written client turns one bug into an outage.

The `202` is returned **before** the write; the pipeline is asynchronous. So
`accepted` counts validation, not persistence, and must not be treated as a
durability receipt.

### Limits

| Limit | Value |
| --- | --- |
| Body | 256 KB |
| Events per batch | 500 |
| Attributes per event | 50 |
| Attribute key length | 64 characters |
| Attribute value length | 512 characters (truncated, not rejected) |

### Origin and CORS

If an `Origin` header is present it must match the project's
`allowed_origins`; if absent, the request is accepted. Native apps send no
`Origin` and are unaffected.

An entry may contain `*`. A bare `*` allows any origin; elsewhere it stands
for any run of characters within the origin, so `https://*.example.com`
matches every subdomain (but not the apex, and not `http://`) and
`https://app.example.com:*` matches any port. The matched origin is echoed
back, never `*`, so a wildcard entry stays compatible with browsers that
reject a literal `*`.

Electron and Tauri renderers **do** send one, so add their scheme
(`tauri://localhost`, `app://.`, `file://`) to `allowed_origins`.

Preflight is answered with `Access-Control-Allow-Headers: Content-Type,
X-Analytics-Key` and echoes the matched origin. To skip preflight entirely,
send the key in the body with `Content-Type: text/plain`, which makes the
request CORS-simple.

### Worked offline queue

**On event**
1. Generate a UUIDv7 `id` and an RFC 3339 `ts` **now**.
2. Append `{id, ts, name, attributes}` to a durable local queue.

**On flush** (app foreground, network available, or a timer)
1. Take up to 500 events from the head of the queue.
2. POST them with the current context as batch-level `attributes`.
3. On `2xx` **or** any `4xx` other than 429: delete those events from the
   queue. A 4xx will never succeed on retry.
4. On `5xx`, 429, or a network error: keep them and retry with exponential
   backoff (for example 1s, 5s, 25s, capped at a few minutes).
5. On `413`: halve the batch size and retry.

**Never**
- Block the UI on a flush.
- Grow the queue without bound — cap it (say 10 000 events) and drop the
  oldest, which keeps recent data when a device is offline for a week.
- Retry a `400` or `401`.

---

## Answer questions with the data

A connected session gets seventeen tools. Reach for a purpose-built one
before `query` — they are cheaper, they cannot be malformed, and they
already apply the caveats below.

**Reading (all take `project_id`, `from`, `to` as `YYYY-MM-DD` unless noted):**

| Tool | Extra parameters | Returns |
| --- | --- | --- |
| `list_projects` | none | Every project with its `project_id`, name, identity mode and data coverage. Call this first — every other tool needs a `project_id` |
| `views_overview` | `kind` (optional) | Visitors, views, sessions, bounces, average session length per day, summed across kinds unless `kind` filters one |
| `views_breakdown` | `dimension`, `limit` (default 20) | Top rows for one of `kinds`, `paths`, `hosts`, `referrers`, `utm`, `countries`, `platforms`, `os`, `browsers`, `app_versions`, `devices`, `displays`. Two-key dimensions return both columns |
| `product_events` | `event` (optional filter) | Count and unique users per event name, plus daily totals |
| `product_attributes` | `event`, `key` | Count, unique users and unique groups per value of a declared attribute. `$platform`, `$os` and `$app_version` are always available; a custom key only appears once the project declares it. `unique_groups` is empty for days rolled up before it was measured and `0` when it was measured and no group was involved |
| `retention` | `actor` (`user` or `install`) | Cohort curves, plus `aggregated_through` — cohorts after that day are **absent, not zero** |
| `identities` | `kind` (`user` or `group`), `limit` | Per-user or per-group activity with display names. **Surfaces personal data on identified projects** |
| `query` | `sql` | A single read-only `SELECT`/`WITH` against the views. Row-capped and time-limited |

**Managing** — `create_project`, `update_project`, `archive_project`,
`restore_project`, `issue_ingest_key`, `list_ingest_keys`,
`enable_ingest_key`, `disable_ingest_key` and `integration_guide`, all
described in [Set up a project](#set-up-a-project).

**Resources:** `docs://twillingate` (this document), `docs://deployment`
(installing and configuring the collector itself), `schema://views` (the
authoritative column list — read it before writing SQL) and
`schema://projects` (the live registry).

Ask in plain language — "how many visitors did myapp get last week by
country", "which hosts is this project collecting from", "which screens do
people hit before they subscribe", "issue a key for the marketing site".

Enabling the endpoint, choosing an auth mode and pointing a client at it are
in [deployment.md](deployment.md#the-api-endpoint).

### HTTP API

Every tool above except `integration_guide` is also a REST route under
`/api/`, guarded by the same bearer token (`Authorization: Bearer …`) as
MCP. Send and receive JSON. `integration_guide` and the `docs://` resources
are MCP-only — there is no REST equivalent.

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://t.example.com/api/projects/1/views/overview?from=2026-09-01&to=2026-09-13"
```

| Method | Path | Mirrors | Input |
|---|---|---|---|
| `GET` | `/api/projects` | `list_projects` | — |
| `POST` | `/api/projects` | `create_project` | body: `name`, `identity`, `allowed_origins`, `attributes`, `skip_key` → 201 |
| `PATCH` | `/api/projects/{project_id}` | `update_project` | body: fields to change (merge); `allowed_origins: []` clears |
| `POST` | `/api/projects/{project_id}/archive` | `archive_project` | — |
| `POST` | `/api/projects/{project_id}/restore` | `restore_project` | — |
| `GET` | `/api/keys` | `list_ingest_keys` | query: `project_id` |
| `POST` | `/api/projects/{project_id}/keys` | `issue_ingest_key` | body: `label` → 201 |
| `POST` | `/api/projects/{project_id}/keys/{label}/disable` | `disable_ingest_key` | — |
| `POST` | `/api/projects/{project_id}/keys/{label}/enable` | `enable_ingest_key` | — |
| `GET` | `/api/projects/{project_id}/views/overview` | `views_overview` | query: `from`, `to`, `kind` |
| `GET` | `/api/projects/{project_id}/views/breakdown` | `views_breakdown` | query: `from`, `to`, `dimension`, `limit` |
| `GET` | `/api/projects/{project_id}/product/events` | `product_events` | query: `from`, `to`, `event` |
| `GET` | `/api/projects/{project_id}/product/attributes` | `product_attributes` | query: `from`, `to`, `event` |
| `GET` | `/api/projects/{project_id}/retention` | `retention` | query: `from`, `to`, `actor` |
| `GET` | `/api/projects/{project_id}/identities` | `identities` | query: `from`, `to`, `kind`, `limit` |
| `POST` | `/api/query` | `query` | body: `sql` |
| `GET` | `/api/schema/views` | `schema://views` | — (text/plain) |

Success is the tool's output as JSON, at 200 (201 where noted above).
Errors are `{"error":{"code":"…","message":"…"}}`: `invalid` is 400,
`not_found` is 404, `conflict` is 409, anything else is `internal` at 500.
A missing or bad token is 401. An unknown query parameter or an unknown
field in a JSON body is also 400 — the same strictness as a malformed one.
So is a query string that does not parse (a bad `%` escape, a bare `;`)
or a parameter given twice: a filter is never dropped silently.

### Writing SQL against the views

The `query` tool takes read-only SQL against the views below. It is
row-capped (`API_QUERY_MAX_ROWS`, default 1000) and time-limited
(`API_QUERY_TIMEOUT`, default `10s`).

**Read `schema://views` for the authoritative column list.** It is kept in
step with the migrations and carries the caveats that cannot be inferred
from the DDL. The three that matter most:

1. `day` columns are TEXT `'YYYY-MM-DD'` (UTC). Compare and `BETWEEN` as
   strings.
2. Every `v_*` view includes yesterday and today: each stitches aggregated
   history (`agg_*` tables) to a live half computed from raw rows.
3. **`v_retention` has no live half.** It refreshes at the 03:00 UTC daily
   pass; cohort days after that are ABSENT, not zero. It is populated only
   for projects with `identity=identified`, because anonymous visitor ids
   rotate daily and cohorts are undefined. Each actor is cohorted by how it
   was identified, `user` (sent a `$user_id`) or `install` (a stable
   `$install_id`); a connection-hash actor cannot be recognised on return and
   is not cohorted. On a client that mints an install id per page load, the
   `install` curve reads near zero — read the `user` curve. Cohorts
   counted before signed-in tracking began (before migration 011) have no
   `user` rows; they sit wholly under `install`.

Every view carries a `project_id` column — always filter on it; the ids
are the ones `list_projects` returns.

The views family is `v_views_daily` (per kind), `v_views_paths`,
`v_views_hosts`, `v_views_referrers`, `v_views_utm`, `v_views_countries`,
`v_views_platforms`, `v_views_os`, `v_views_browsers`,
`v_views_app_versions` (keyed by `platform` and `app_version`),
`v_views_devices` and `v_views_displays`. Every dimension is capped at 500
values per day; the tail is one `(other)` row whose visitors are distinct
actors, not a sum. `os`, `browser` and `device` are lower-case closed
vocabularies (see [Declaring the environment](#declaring-the-environment))
in which `other` and `(other)` are different things: `other` is a real
value outside the list, `(other)` is the cap.
Product events have `v_product_daily`, `v_product_totals` and
`v_product_attrs` (whose `unique_groups` is NULL, not zero, for days rolled
up before it was measured — `MAX()` skips it, `SUM()` would too, a `COALESCE`
to 0 would lie), plus `v_events_flat`, which reads the `events` table
with one column per declared attribute. `v_identity_daily` and `identities` join user and group
activity to display names; `v_identity_daily` keeps the busiest 500 users
and 500 groups per day and drops the rest, with no `(other)` row, so do not
sum it for totals. `v_retention` is keyed by `actor_kind`.

Cost note: the views' live halves sessionize raw rows with window functions,
and a `WHERE` on `day` may not prune that work. Narrow ranges and the `agg_*`
tables are cheaper.

---
