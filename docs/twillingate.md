# twillingate

What an AI agent — or a person — needs to set up a project, get a site or app
sending data, and answer questions from what comes back. The MCP endpoint
serves this file verbatim as `docs://twillingate`.

Installing twillingate, configuring the collector, enabling the API endpoint
and backing up the database are the operator's job, in
[deployment.md](deployment.md).

- [What twillingate is](#what-twillingate-is)
- [Set up a project](#set-up-a-project)
- [Instrument a website](#instrument-a-website)
- [The event model](#the-event-model)
- [The wire format](#the-wire-format)
- [Answer questions with the data](#answer-questions-with-the-data)

---

## What twillingate is

One Go binary, one SQLite file. It collects views (page views from websites,
screen views from apps and CLIs) and custom product events through one endpoint,
rolls them up nightly, and exposes the result as dashboards at `/app/`, a
read-only SQL surface, and an API (MCP or HTTP). It is cookieless by default:
the served SDK's `anonymous` default writes nothing to a visitor's device
unless the tag declares consent, and then only its retry queue, never an
identifier. The collector stores the identifiers it is sent; a visitor who
sends none is a hash of the connection under a key that rotates at midnight.
IP addresses and User-Agent strings are never stored — the IP becomes a
country at ingest, the User-Agent is checked for crawlers, and both are then
discarded.

| Command | Does |
| --- | --- |
| `twillingate serve -ingest` | Ingestion: `POST /ingest/events`, the SDK at `/js/twillingate.js` (and its Web Vitals add-on at `/js/twillingate-vitals.js`), `/healthz` |
| `twillingate serve -api` | The API endpoint: MCP at `/mcp`, REST at `/api/`, the dashboards at `/app/` |
| `twillingate serve` | Both, on one listener unless `API_ADDR` says otherwise |
| `twillingate project`, `key`, `config` | Registry management |
| `twillingate migrate` | Applies schema migrations and exits |

Which of those run, and how, is the operator's choice — see [Configure the
collector](deployment.md#configure-the-collector).

---

## Set up a project

Projects live in a registry table, managed through the CLI or, over the API
(MCP or HTTP), through the management tools.

| Operation | CLI | MCP tool | Tool arguments |
| --- | --- | --- | --- |
| Create a project | `twillingate project create` | `create_project` | `{name, allowed_origins, attributes}`; `name` required; `skip_key: true` issues no first key; returns the new `project_id` |
| Change one | `twillingate project update` | `update_project` | `{project_id, …}`; merges, so an omitted field keeps its value and `allowed_origins: []` clears the list |
| List them | `twillingate project list` | `list_projects` | none |
| Archive / restore | `twillingate project archive` / `restore` | `archive_project` / `restore_project` | `{project_id}` |
| Issue an ingest key | `twillingate key issue` | `issue_ingest_key` | `{project_id, label}`; returns the key **and** a paste-ready snippet |
| List keys | `twillingate key list` | `list_ingest_keys` | `{project_id}` |
| Disable / enable a key | `twillingate key disable` / `enable` | `disable_ingest_key` / `enable_ingest_key` | `{project_id, label}` |
| Get paste-ready setup | — | `integration_guide` | `{project_id, platform}` where platform is `web`, `spa`, `server` or `mobile`; returns the whole setup as markdown with the live key filled in. Reach for it before hand-assembling a snippet |

**Confirm the collector hostname before pasting.** Snippets use `PUBLIC_URL`,
but a collector can answer on several hostnames; ask which one this site uses
and change the snippet's `src`. The SDK posts to the origin it was loaded from.
There is no rename (`project update -name` is one) and no delete over the API —
deletion needs the CLI. Archiving is reversible with `restore_project`, but not
forever: the daily pass deletes an archived project, dashboard or widget (and,
for a project, all its data) once it has been archived longer than
`RETENTION_ARCHIVED_DAYS` (default 30; 0 keeps archived items forever).

```bash
twillingate project create -name "My App" \
  -origin https://myapp.com -attr plan -attr tier
twillingate project list                                 # id  name
twillingate project update -id 1 -origin https://myapp.com -origin https://www.myapp.com
twillingate project update -id 1 -clear-origins
twillingate project archive -id 1                        # reversible: `project restore`
twillingate key issue -project-id 1 -label web
twillingate key list -project-id 1
twillingate key disable -project-id 1 -label ios-2025
```

`-origin` and `-attr` replace the whole list rather than merging;
`-clear-origins` (or `allowed_origins: []`) empties the origins.

| Key | Meaning |
| --- | --- |
| `project_id` | Integer key assigned on create, never reissued after a delete. The `project_id` column on every stored row, the argument of every tool, route and CLI command, and the segment of every dashboard URL. Never transmitted by clients. |
| `name` | Display name. Required; free text, need not be unique; change it with `project update -name`. |
| `ingest_keys` | One or more `{key, label, disabled}` credentials. Required. |
| `allowed_origins` | Origins allowed to post for this project. `*` is a wildcard — `https://*.example.com` covers every subdomain, a bare `*` allows any origin. Add `tauri://localhost` or `app://.` for Electron/Tauri. |
| `attributes` | Custom product-event attribute keys to break down. |

### Attribute breakdowns

```bash
twillingate project update -id 1 -attr plan -attr tier
```

A declared key gets a value breakdown (counts and unique users/groups per
value, per event, per day) in `agg_product_attrs` / `v_product_attrs`. A
declared custom key also gets an `attr_*` column in `v_events_flat`; a declared
reserved key is already a typed column there. Undeclared keys are still
stored and reachable via `json_extract(attributes, '$.junk')`; declaring one
later does not backfill. `PRODUCT_ATTRIBUTES_TOP_N` (default 50, set
[server-side](deployment.md#configure-the-collector)) keeps the top N values per
key and collapses the tail into one `(other)` row whose unique counts are
recomputed from raw, so **a client sending the literal `(other)` loses its own
count**. Never declare an unbounded custom key such as a session id. `$platform`,
`$os`, `$app_version`, `$app_locale`, `$kind`, `$browser`, `$device` and
`$browser_locale` roll up automatically and need no declaration. Nine more
reserved keys can be declared like a custom key to get the same per-value
breakdown: `$host`, `$path`, `$referrer`, `$utm_source`, `$utm_medium`,
`$utm_campaign`, `$os_version`, `$browser_version` and `$device_model`. The
top-N cap keeps a declared `$path` bounded. Declaring any other `$` key is
refused. Rollups run whether or not a project declares attributes; declaring
only adds the per-value breakdown and the `attr_*` columns.

### Ingest keys

A project needs at least one key, and the key identifies the project — no
payload carries a project field. Keys are **public by design**: they ship in
binaries and page source, so their job is revocation, not secrecy. To retire
one, add the replacement, ship clients, watch the old label fall to zero in the
per-minute `ingest summary` log line, then disable it. Disabling is reversible;
deleting the entry is the eventual cleanup.

---

## Instrument a website

The collector serves its own SDK at `/js/twillingate.js`, compiled from the
TypeScript in `sdk/` and embedded in the binary. The served file carries the
collector's origin, so nothing but the key is configured. Load it as a tag or
inject the same tag from code; bundling the module is not supported.

### Snippet mode

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…"></script>
```

`twillingate key issue -project-id <id> -label <label>` prints this snippet
ready to paste, with `src` on `PUBLIC_URL`; the served copy posts back to
whichever hostname loaded it.

| Attribute | `init()` option | Meaning |
| --- | --- | --- |
| `data-key` | `key` | The project's ingest key. Required to auto-init; without it the tag loads dormant for `init()` from code. Public by design. |
| `data-identity` | `identity` | `anonymous` (default) or `identified`. Decides what the instance **sends**: anonymous never sends `$user_id`, `$user_name` or `$install_id`, and `identify()` is inert; identified sends them and, with [consent](#consent-and-storage), persists the visitor id, user and group. |
| `data-auto="off"` | `autoPageviews` | Disable automatic pageviews; drive them with `twillingate.page()`. Both default on. |
| `data-mask-url` | `maskUrl` | Rewrite the URL before it is sent. See [Masking](#masking-urls). |
| `data-routing` | `routing` | `history` (default) or `hash`. See [Routing](#routing). |
| `data-kind` | `kind` | What this client is: `web` (default), `app`, `cli`, or any short lower-case token. Anything but `web` switches automatic tracking from `$page_view` to `$screen_view` (the route path becomes the screen) and exempts the client from the server's crawler filter. |
| `data-consent` | `consent` | May this instance keep anything on the device. `false` (default): nothing is read from or written to storage. `true`, or the name of a global variable or function a consent manager maintains, unlocks it. See [Consent and storage](#consent-and-storage). |
| `data-vitals` | `vitals` | Sample rate for [Web Vitals](#web-vitals), in `(0, 1]`: `1` measures every page load, `0.2` one in five. Absent (or anything outside the range) = off. |
| `data-instance` | `create(name)` | Register this tag's instance as `twillingate.get(name)` instead of as the default instance. See [Two projects on one page](#two-projects-on-one-page). |

Every `data-*` has an `init()` equivalent except `data-instance`, which maps to
`create()`'s name; the reverse does not hold — `flushInterval`, `platform`,
`appVersion`, `appLocale`, `autoAttributes`, `storage`, `taggedEvents`, `optOut` and `debug` are code-only
options with no `data-*` form, and identity is set from code (`identify`,
`group`, `installId`), never in markup. Views are automatic, including on
`history.pushState` and `popstate`; elements carrying `data-twillingate-event`
are tracked on click or submit. Include each tag once: a duplicate with the same
`data-key` or with none is ignored with a warning, one with a different key
replaces the default instance, also with a warning, and a second project uses
`data-instance`. The collector also serves `/js/plausible-shim.js`, which fires
events from Plausible's `plausible-event-*` classes — see
[docs/plausible/](plausible/).

### From code

Loaded without `data-key` the file stays dormant until `init()`; a script
injected from code behaves the same, and setting `s.dataset.key` before
appending it makes it auto-init as a pasted tag would.

```js
twillingate.init({
  key: "ak_9f3c…",             // required; the collector's origin is in the file
  identity: "anonymous",       // or "identified": decides what is sent
  consent: false,              // default; true, or a global's name, unlocks storage
  storage: "localStorage",     // default; sessionStorage, memory, cookie, or a driver
  autoPageviews: true,         // default; false for programmatic page() calls
  taggedEvents: true,          // default; false to ignore data-twillingate-event markup
  maskUrl: "uuid",             // built-ins, a RegExp, or a function
  routing: "history",          // or "hash"
  kind: "app",                 // → $kind ("app" for Electron/Tauri, "cli", …)
  platform: "electron",        // → $platform; defaults to "web" only for kind "web"
  appVersion: "2.4.1",         // → $app_version
  appLocale: "de",             // → $app_locale; never detected
  autoAttributes: true,        // default; false sends only what you set (see Precedence)
  flushInterval: 10000,        // milliseconds
  vitals: 0.2,                 // Web Vitals sample rate in (0, 1]; absent = off
  optOut: () => location.hostname === "localhost",   // OR-ed with twillingate_ignore
  debug: false,                // OR-ed with the twillingate_debug flag
});
```

`init()` returns the instance; a second `init()` on a live one warns and is
ignored. **Every call is legal before `init()`**: `onPage`, `onEvent`, `attrs`,
`identify`, `group`, `installId`, `consent`, `optOut` and `debug` apply at once,
while `page`, `screen`, `track` and `flush` are held in order (capped at 500,
oldest dropped) and run right after `init()`, entry pageview first. Identity set
before `init()` stands; stored values load only for what was not set.

```js
const et = twillingate.create("econumo");   // dormant until init()
et.identify(userHash); et.group(workspaceId);
et.init({ key: "ak_econumo…", identity: "identified", autoPageviews: false });
```

```js
twillingate.measure("checkout_api", 340, "time", { endpoint: "/api/checkout" });
```

### Runtime API

| Call | Meaning |
| --- | --- |
| `init(opts)` | Start the instance with the options above. Returns the instance. |
| `page(arg?, attrs?)` | `$page_view`. No argument records the current page; a string records that path, with no campaign parameters; an object is extra attributes for the current page. `page(fn)` still registers a pageview listener, with a deprecation warning. |
| `onPage(fn)` | A pageview listener, automatic pageviews included. See [Listeners](#listeners). |
| `onEvent(fn)` | Runs for every event, after `onPage` has finished with a pageview. Receives `{ name, attributes }`; return attributes to merge, `false` to drop the event, anything else to observe. A listener that throws drops the event with a warning. |
| `screen(name, attrs?)` | An explicit `$screen_view`, on any kind. |
| `track(name, attrs?)` | An opt-in product event. Don't `$`-prefix your own names. |
| `measure(name, value, measure, attrs?)` | Records a measure: `measure` is `"time"` (ms), `"size"` (bytes) or `"number"`; invalid calls are dropped (logged with `debug`). |
| `attrs(obj)` | Default attributes under every event. Successive calls merge; `attrs(null)` clears. |
| `identify(user, name?)` | Sets `$user_id` and the optional `$user_name`. Inert on an anonymous instance, with one warning; persisted on an identified instance with consent. Events already sent stay unattributed. |
| `group(id, name?)` | Sets `$group_id` and the optional `$group_name` in every mode; persisted with consent on an identified instance. |
| `installId(id?)` | The stable per-install id an app supplies as `$install_id`. With no argument returns what would be sent: the declared id, else the persisted visitor id, else `null`. Inert on an anonymous instance. |
| `reset()` | **Required on logout.** Clears user, group, names, the visitor id and the retry queue. |
| `flush()` | Force-send the queue. |
| `consent(granted?)` | `true` / `false` pins storage consent over whatever was declared, `null` hands control back, no argument reads it. See [Consent and storage](#consent-and-storage). |
| `optOut(flag?)` | `true` writes `twillingate_ignore`, `false` clears it, no argument reads. Returns the effective state, the `optOut` callback included. |
| `debug(flag?)` | `true` writes `twillingate_debug`, `false` clears it, no argument reads. Returns the effective state. See [Debugging](#debugging). |
| `twillingate.create(name, opts?)` | A second instance; with options it also initialises it. See [Two projects on one page](#two-projects-on-one-page). |
| `twillingate.get(name?)` | Look an instance up from anywhere; no name is the default instance. |
| `util.maskIds(value, opts?)` | Mask ids in a path or URL. See [Masking](#masking-urls). |
| `util.withQuery(path, url, keys)` | Append allowlisted query parameters, sorted. See [Routing](#routing). |
| `detectOS()`, `detectBrowser()`, `detectDevice()` | Read the environment this client would report. See [Detection](#detection). |

**Precedence**: SDK-derived values (`$host`, `$path`, `$referrer` and campaign
parameters on views; the page's `$host` and `$path`, or `$screen` on a non-web
kind, on product events), then
`attrs()` defaults, then the call's attributes, then listener returns in
registration order. Later layers win, and a `null` drops the key wherever it
came from: `twillingate.attrs({ $host: "selfhosted_ab12", $referrer: null })`
sends that host and no referrer. A batch attribute (`$os`, `$browser`, display
size, …) set to `null` is sent as `null` on the event, which the collector reads as not
sent. A `null` on a `$` prefix drops the family: `$utm: null` drops
`$utm_source`, `$utm_medium` and `$utm_campaign`, and `$browser: null` drops
`$browser`, `$browser_version` and `$browser_locale`. A later layer's
explicit value still beats an earlier family `null`. `autoAttributes: false`
sends none of the derived values except what a view needs to exist (`$host`
and `$path`, or `$screen`), plus `$kind`, `$platform` for the web kind,
`$consent` and identity. A product event carries the location of the last
view this instance sent — after `maskUrl`, any `onPage` redaction and any
`onEvent` rewrite; a view an `onEvent` listener drops is not remembered — so
a recipe that redacts a pageview's path (`/account/12` → `/account/[id]`)
redacts product events the same way; before the first view,
it carries a `maskUrl`-derived location only when no `onPage` listener is
registered.

### Two projects on one page

`window.twillingate` is the default instance and a factory. `create(name,
opts?)` builds a second instance, prefixes its storage keys with the name
(`et_visitor`, `et_user`, `et_queue`, …), registers it and, with options,
initialises it. A name already registered returns the existing instance, warning
if options are passed; names must match `^[a-z][a-z0-9_]{0,15}$` and no
`window.<name>` is created.

```js
const et = twillingate.create("et", { key: "ak_app…", identity: "identified" });
twillingate.get("et").track("export");
```

A tag can do the same, with or without `data-key`.

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_web…"></script>
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_app…" data-instance="et" data-auto="off"></script>
```

Browser hooks install once whatever the instance count: one `pushState` patch,
one set of `popstate`, `hashchange`, `online`, `pagehide`, `visibilitychange`
and tagged-element listeners. Each instance applies its own settings —
`autoPageviews` off ignores navigations, history routing ignores `hashchange`,
`taggedEvents` off ignores markup. `twillingate_ignore` and `twillingate_debug`
stay global and unprefixed.

### Consent and storage

`consent` answers one question: may this instance keep anything on the device.
Default `false`: records live in memory, nothing is read or written except the
`twillingate_ignore` and `twillingate_debug` flags, anything an earlier session
stored under this instance's keys is deleted, and a failed batch is retried from
memory (on `online`, once more via `sendBeacon` when the page hides or unloads)
and lost if the tab closes first. With consent an `identified` instance persists
the visitor id, user and group in `twillingate_visitor` (or
`<instance>_visitor`), and any instance persists the retry queue; an `anonymous`
instance sends no identifier, so consent only unlocks its queue.

| `data-consent` / `consent` | Meaning |
| --- | --- |
| absent, `""`, `false`, `"false"` | no consent — the default |
| `true`, `"true"` | consent given |
| a function, or a string naming one on `window` | called whenever consent is consulted; the return value is coerced to a boolean |
| a string naming a non-function global | read whenever consent is consulted, so a variable the consent manager flips is picked up live |

A name resolving to nothing, or a function that throws, fails closed with a
console warning. Consent is consulted at every storage decision, never cached:
flipping to true writes the waiting memory queue to storage and starts
persisting identity, flipping to false deletes every key the instance owns.

Every batch carries `$consent` — `1` or `0`, the answer in force when it was
sent — in both identity modes, so the `consent` breakdown (`given`, `none`,
`unknown`) shows how many visitors consented; `none` also catches a view sent
before the visitor answered, so read it as a trend, not an acceptance rate.

**Where keys live** is the `storage` option:

| `storage` | Meaning |
| --- | --- |
| `"localStorage"` (default) | the browser's localStorage |
| `"sessionStorage"` | identity and queue live for the tab |
| `"memory"` | nothing on the device; equivalent to no consent |
| `"cookie"` | one host-only cookie per identity key, `SameSite=Lax`, one year, `Secure` on https; the retry queue stays in memory |
| `{ get(key), set(key, value), remove(key) }` | a custom driver; a driver that throws counts as unavailable |

```js
const cookies = {   // a cookie on a shared parent domain is a custom driver
  get: (k) => document.cookie.match(new RegExp("(?:^|; )" + k + "=([^;]*)"))?.[1] ?? null,
  set: (k, v) => { document.cookie = `${k}=${v}; domain=.example.com; path=/; max-age=31536000; SameSite=Lax; Secure`; },
  remove: (k) => { document.cookie = `${k}=; domain=.example.com; path=/; max-age=0`; },
};
twillingate.init({ key: "ak_…", identity: "identified", consent: true, storage: cookies });
```

The SDK does not decide where analytics runs — `localhost`, `file://` and
automated browsers are tracked like anything else; keep development traffic out
with `optOut`, and opt a device out with `twillingate.optOut(true)` or
`localStorage.twillingate_ignore = "true"`.

### Detection

```js
twillingate.detectOS();       // { os: "ipados", osVersion: "17.2", osName: "iPadOS 17.2" }
twillingate.detectBrowser();  // { browser: "brave", browserVersion: "126" }
twillingate.detectDevice();   // { device: "tablet" }
```

The SDK detects these on the client and sends them on every batch — `$os`,
`$browser` and `$device` always, `$os_version`, `$os_name` and
`$browser_version` when determinable — and detection is the only source, with no
override. iPadOS in desktop mode and Brave are the two cases a User-Agent cannot
tell apart; Windows 11 and true macOS versions come from
`navigator.userAgentData.getHighEntropyValues`, started at `init()` and read at
flush. Each function takes an optional `ClientSignals` (`userAgent`, `platform`,
`maxTouchPoints`, `brave`, `brands`, `uaPlatform`, `mobile`, `platformVersion`)
and then consults only what it contains. `other` means a User-Agent naming
nothing on the list, `unknown` means no User-Agent at all, and `watchos`,
`visionos` and `wearable` are never returned. `$platform` is never detected: it
is the option, or `web` while `kind` is `web`, or absent.

### Masking URLs

A pageview carries its location **already split** into `$host` and `$path`,
stored verbatim. `data-mask-url` receives `location.href`, returns a URL string,
and the SDK splits that; it resolves inside `init()` **before the first
pageview**, so the entry page is covered. Three value forms:

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

`init({ maskUrl })` also accepts a `RegExp` or a function directly. **Campaign
parameters are read from the original href before the mask runs.** **Masking
fails closed, loudly:** a missing named function, an unparseable regexp, or a
mask that throws or returns a non-string drops pageviews with one
`console.warn`.

```js
twillingate.util.maskIds(value, opts?)      // uuid + ulid by default; { numeric, hex } opt-in
twillingate.util.withQuery(path, url, keys) // allowlisted query params, sorted
```

`maskIds` is segment-by-segment and URL-aware, masking path and hash segments
and never the host; numeric is opt-in because `/2024/annual-report` is a real
path, and `hex` covers 24+ character blobs.

#### Listeners

```js
twillingate.onPage(({ path }) => ({ $path: path.replace(/^\/account\/[^/]+/, "/account/[id]") }));
twillingate.onPage(({ path }) => ({ $path: path.replace(/\/orders\/\d+/, "/orders/[id]") }));
// /account/88/orders/12 → /account/[id]/orders/[id]
```

- A listener receives `{url, host, path, referrer, attributes}` and returns
  attributes to merge or `false` to cancel. **Values thread through the chain**:
  each listener receives the previous one's `host` and `path`.
- `url` is the **post-mask** URL, not `location.href`.
- `false` cancels and later listeners do not run; a listener that throws, or
  produces no `$path`, drops the pageview with a warning.
- The entry pageview is emitted **synchronously** during `init()`, so a listener
  registered before `init()` covers it and one registered after cannot, with a
  warning. In snippet mode with a key `init()` runs when the tag executes — use
  `data-mask-url` to cover the entry page.

### Tagged elements

```html
<button data-twillingate-event="signup">Start free trial</button>
<form data-twillingate-event="newsletter">…</form>
```

The event fires on click (main or middle button) or, for a `<form>`, on submit.
The nearest tagged ancestor of the click target wins; `path` is added as an
attribute and nothing else is read off the element. The listeners run in the
capture phase, so a handler that stops propagation cannot eat the event. Every
instance with `taggedEvents` on (the default) tracks it.

### Routing

```
https://shop.example.com/app/?utm_source=news#/account/3f8a…/edit?tab=billing
  → { $host: "shop.example.com", $path: "/app/#/account/[id]/edit", $utm_source: "news" }
```

`data-routing="hash"` is for routes living in `location.hash`: `$path` becomes
`pathname + hash` with the query stripped from both, `hashchange` emits a
pageview (hash mode only), dedup keys on `pathname + search + hash`, and
`util.maskIds` masks hash segments too. In history mode `pushState` is hooked
and the dedup key includes `location.search`, so query-only navigations fire
distinct pageviews. `$path` carries no query string by default; appending one is
an explicit opt-in, and only for low-cardinality parameters:

```js
twillingate.onPage(({ url, path }) => ({
  $path: twillingate.util.withQuery(path, url, ["tab", "view"]),
}));   // → /settings?tab=billing
```

### Web Vitals

Off unless asked for: `data-vitals="0.2"` on the tag, or `vitals: 0.2` in
`init()`, turns them on for that instance (web kind only). A page load that
has them on sends each Core Web Vital as a [measure](#measures): `$lcp`,
`$inp`, `$fcp` and `$ttfb` in milliseconds (`time`), `$cls` as a `number`.
Every one carries `$sample_rate` (the configured rate, `1` included) and,
unless `autoAttributes` is false, the `$host` and `$path` of the page load it
measures, masking applied; a
single-page app's later route changes do not move them, and INP and CLS cover
the page's whole life, as Chrome counts them. The `attrs()` defaults apply;
identity, consent, `optOut` and `debug` work as for any other event.

Sampling is decided once per page load: one random draw against the rate,
after which the page sends every vital it produces, or none, so a sampled page
is complete. Two instances on one page each have their own rate and draw.

The numbers come from Google's [`web-vitals`](https://github.com/GoogleChrome/web-vitals)
package, in a second script the collector serves at `/js/twillingate-vitals.js`
(≈3.9 KB gzip, Apache-2.0). The SDK loads it from the same origin only when an
instance enables vitals, once per page however many do, so a site without
vitals downloads nothing more. FCP and TTFB are queued as soon as they are
known; LCP, CLS and INP arrive when the page is hidden (a tab switch, a
navigation, a close), and a vital reported then goes out at once through
`sendBeacon` rather than waiting for the timer. Each vital is sent once per
page load, with its value at the first time the page is hidden; INP and CLS
growth after that (a tab switch and return) is not sent. A page restored from
the back/forward cache reports its vitals again.

### Transport

Events queue for up to 10s and flush as one batch: on the timer, once 20 events
accumulate, on `flush()`, and on page unload (`pagehide` / `visibilitychange`
via `sendBeacon`, the key in the JSON body because beacons cannot set headers).
Every event carries a UUID, a client timestamp and its `family` — `views` for
`page()`/`screen()`, `product` for `track()` and tagged elements, `measures`
for `measure()` — sent explicitly, never left for the collector to infer.
The environment (`$os`,
`$browser`, display size, …) goes once per batch, and any attribute every event
in a batch carries with the same value (usually the `attrs()` defaults and
`$host`) is moved up to the batch too; the collector lays batch attributes
under each event's, so what it stores is the same and only the body shrinks. A batch that fails (network
down, 5xx) is kept for retry — in memory, or with
[consent](#consent-and-storage) in a bounded stored queue (`twillingate_queue`
or `<instance>_queue`, 50 batches) — and replays on `online`, once more via
`sendBeacon` on unload, and from storage on the next load. Replays dedupe
server-side by event id; a 4xx drops the batch instead.

### Debugging

```
[twillingate] $page_view { $host: "shop.example.com", $path: "/account/[id]" }
[twillingate] sent 1 event(s) → 202
[twillingate:et] budget_created { currency: "EUR" }
```

`localStorage.twillingate_debug = "true"` in the console, `debug(true)`, or the
`debug` option logs every event and every send of every instance, prefixed with
the instance name, without changing what is sent.

---

## The event model

Everything goes to one endpoint, `POST /ingest/events`. `family` decides
which family an event lands in; when it is omitted, the event's **name**
decides instead:

| name | family | default `$kind` | feeds |
| --- | --- | --- | --- |
| `$page_view` | views | `web` | the views dashboard, `views_overview`, `views_breakdown`, retention |
| `$screen_view` | views | `app` | same |
| anything else | product | — | `product_events`, `product_attributes` |
| any name, with `family: "measures"` | measures | — | `measures`, the Web Vitals and Measures dashboards |

All three families are stored in one raw table, `events`, whose `family`
column is `views`, `product` or `measures`; the aggregates, views and tools
of each family read only its own rows.

The `$` prefix is reserved for the system. An unrecognized `$` **name** is
stored as an ordinary custom event with a warning; an unrecognized `$`
**attribute key** is dropped, with a warning in the response body.

### Views (`$page_view`, `$screen_view`)

A view is one page or screen shown to someone, and both names store the same
row. `$kind` overrides the default kind with any token matching
`^[a-z][a-z0-9_]{0,15}$`, usually as a batch attribute; an invalid value warns
and the default is used. Every view, of any kind, is enriched with a country
derived from the connection's IP; only `web` has further server-side meaning —
a web view (and a web measure) is also filtered for a crawler User-Agent,
while no other kind is filtered. `$path` (or its alias `$screen`) is
**required** and `$host` optional, both stored verbatim, and `$path` may
contain a `#` (hash routing) or a `?` (query routing). Campaign parameters are
`$utm_source`, `$utm_medium` and `$utm_campaign`; `$referrer` is reduced to a
source name and dropped on web views as a self-referral when its host matches
`$host`. A client `$session_id` is authoritative, otherwise a gap over 30
minutes per actor starts a session, and a bounce is a single-view session —
expect high bounce rates on app kinds.

The IP and the User-Agent are never stored, on any kind: the IP becomes the
country, the User-Agent is checked for a crawler and discarded — the only
User-Agent text kept is the `$os_name` a client declares. **A backend must
not relay web views for other people**: they would all carry the backend's
IP (one country for everyone) and its User-Agent, which the crawler filter
drops for curl or an HTTP library.

### Declaring the environment

| Key | Values | Absent | Unrecognised |
| --- | --- | --- | --- |
| `$platform` | Open. Trimmed, lower-cased, then `^[a-z][a-z0-9_]{0,15}$`. Conventionally `web`, `ios`, `android`, `macos`, `windows`, `linux`, `electron`, … | `unknown` | `unknown`, with a warning |
| `$os` | `windows` `macos` `linux` `bsd` `chromeos` `ios` `ipados` `android` `fireos` `harmonyos` `kaios` `tvos` `watchos` `visionos` `tizen` `webos` `playstation` `xbox` `nintendo` `other` `unknown` | `unknown` | `other`, with a warning; the raw value is kept in `os_name` |
| `$browser` | `chrome` `safari` `firefox` `edge` `opera` `samsung_internet` `brave` `vivaldi` `duckduckgo` `yandex` `other` `unknown` | `unknown` | `other`, with a warning |
| `$device` | `desktop` `mobile` `tablet` `wearable` `xr` `other` `unknown` | `unknown` | `other`, with a warning |

**The client declares its environment; the server validates and never parses.**
Those four keys plus `$os_version`, `$os_name`, `$browser_version`,
`$device_model`, `$app_version`, `$app_locale`, `$browser_locale`, `$display_width` and
`$display_height` are stored on any kind exactly as declared: the JS SDK sends
them on every batch (see [Detection](#detection)), and any other client sends
them itself or records `unknown`. `$platform` is the surface the product is used
through and `$os` the operating system it runs on — Safari on an iPhone is
`platform=web, os=ios`, an Electron build on a Mac is `platform=electron,
os=macos` — while `$kind` is adjacent but coarser. Validation of `$os`,
`$browser` and `$device` trims, lower-cases and folds spaces and dashes
(`Chrome OS` → `chromeos`, `Samsung Internet` → `samsung_internet`) and never
rejects; **`other` and `unknown` are different answers**, `other` being a value
outside the list and `unknown` no information at all — both are in the closed
vocabulary themselves, so a client sending either literally is a recognised
value and draws no warning, unlike an unrecognised string, which folds to
`other` with one.

`$os_name` is the OS's full self-reported name with version (`macOS 14.2`,
`Windows 11`), stored verbatim on views, never aggregated and never a
breakdown dimension, so an `$os` of `other` stays investigable through
`query`; it is empty when absent rather than defaulted. `$os_version`,
`$browser_version` and `$device_model` are free text, and `$device` is the
form factor, with consoles and TVs as `other` because `$os` already names
them. The JS SDK sends the major browser version and the OS version it can
determine (see [Detection](#detection)); what it cannot determine stays absent.

`$app_version` is the version of whatever client sent the event, on any
kind — a web build as readily as a native app's. `$app_locale` is the
language that client shows the product in, as the product names it (`de`,
`pt-BR`); `$browser_locale` is the language the browser or OS asks for
(`navigator.language`, which the JS SDK sends on every batch). Both are free
text stored as sent; the SDK never guesses `$app_locale`, so it is sent only
when `appLocale` is set. `$locale` is not a key any more: it is dropped with
an unknown-key warning. Views and product events store every one of these
keys, validated the same way; the JS SDK sends them on every batch.

### Product (everything else)

Any other name is a custom product event; don't `$`-prefix your own names.
Attributes are free-form — see [Attribute breakdowns](#attribute-breakdowns) for
which keys get their own column and value breakdown.

```js
twillingate.track("signup", { plan: "pro" });
```

### Measures

A measure is a name, a numeric value and a time: `measure("checkout_api", 340,
"time")`. Send `family: "measures"` explicitly — it is never inferred, so a
product event carrying a number stays a product event. `value` is a JSON
number from `0` to `1e15` (about 31,000 years in milliseconds, 1 PB in
bytes; a larger one is rejected as a client bug); `measure` sets the kind and
its fixed unit:

| `measure` | Unit | Examples |
| --- | --- | --- |
| `time` | milliseconds | request duration, LCP |
| `size` | bytes | payload, file, memory |
| `number` | none | queue depth, retries, CLS |

Money is not a kind: send it as a `number` and put the currency in the name
(`cart_value_eur`).

Web Vitals are reserved metric names, each with the one kind it may be sent
as:

| Name | Metric | `measure` |
| --- | --- | --- |
| `$lcp` | Largest Contentful Paint | `time` |
| `$inp` | Interaction to Next Paint | `time` |
| `$cls` | Cumulative Layout Shift | `number` |
| `$fcp` | First Contentful Paint | `time` |
| `$ttfb` | Time to First Byte | `time` |

A reserved metric sent with the wrong kind is rejected; an unrecognized `$`
metric name is stored with a warning, like any other unrecognized `$` name.
`$sample_rate` (a number in `[0.0001, 1]`) is read on measures only, and is
what lets a page or backend send a fraction of its measures and still have the
count and mean come out right: each stored row counts as `1/rate`. A rate
below `0.0001` would let one row outweigh 10,000 unsampled ones, so it is
stored as `1` with a warning, like any other invalid rate.

A measure only exists when the event says `family: "measures"`. One that
declares `$kind: "web"` (every Web Vital does) is dropped, still accepted,
when the User-Agent is a crawler, as a web view is; a backend's measures
declare no kind and are never filtered.

### Identity

| The tag says | `$user_id`, `$install_id`, `$user_name` | `$group_id`, `$group_name` |
| --- | --- | --- |
| `anonymous` (default) | never sent | sent, stored raw |
| `identified` | sent, stored as given | sent, stored raw |
| a client posting by hand | stored as given, whatever it sends | stored raw |

The actor resolves as `$user_id` → `$install_id` → a server-side hash of the
connection; how it was identified is recorded alongside it (`user`, `install` or
`connection`) and is what retention cohorts on, so only user- and
install-identified actors are tracked. Send `$install_id` only if it survives a
page load or app restart: an id minted per load makes every load a new actor
that never returns, inflating active counts and dragging retention toward zero,
so leave it out — the connection hash then gives one actor per device per day —
and read retention from the `user` cohort. `$group_id` is stored raw because it
identifies an organization, not a natural person; single-person groups are
personal data. `$user_name` is kept only beside a `$user_id`. **The collector
stores what it is sent.** What reaches it is decided by the tag's
`data-identity` (or `identity` in code), so a project's privacy posture is the
posture of its clients. The collector logs `project receives ids` the first
time a project stores a `$user_id` or `$install_id` (once per kind per process),
which is how to confirm a marketing site's tag sends nothing.

---

## The wire format

Normative. Three independently written clients (iOS, Android, desktop) must
behave identically from this section alone.

### Endpoint

`POST /ingest/events` is the only ingest endpoint. There is no separate
pageview, event or batch path — a single event is a batch of one.

### Authentication

| Client | How the key travels |
| --- | --- |
| Apps, server-side | `X-Analytics-Key: ak_…` header (preferred) |
| Browsers | `"key"` field in the JSON body |

The key identifies the project, so **no payload carries a project field**. The
header wins when both are present, and browsers must use the body because
`navigator.sendBeacon` cannot set custom headers and beacons are the only
transport that survives page unload. An unknown key gets a plain `401`.

### Envelope

```jsonc
{
  "key": "ak_9f3c…",             // omit when using the header
  "attributes": {                // batch-level defaults, all optional
    "$install_id": "018f1e5a-…", "$user_id": "u_123", "$user_name": "Ada Lovelace",
    "$group_id": "org_9", "$group_name": "Acme Corp", "$session_id": "018f1e5b-…",
    "$consent": 1,
    "$kind": "app", "$platform": "ios", "$os": "ios", "$os_version": "17.2",
    "$os_name": "iOS 17.2", "$browser": "safari", "$browser_version": "17",
    "$device": "mobile", "$device_model": "iPhone15,2", "$app_version": "2.4.1",
    "$browser_locale": "en-US", "$app_locale": "de", "$display_width": 1179, "$display_height": 2556
  },
  "events": [
    { "id": "018f1e5c-…", "ts": "2026-08-30T10:00:00Z", "name": "$page_view",
      "attributes": { "$host": "shop.example.com", "$path": "/account/[id]/edit",
                      "$utm_source": "newsletter",
                      "$referrer": "https://news.ycombinator.com/" } },
    { "id": "018f1e5d-…", "ts": "2026-08-30T10:00:05Z",
      "name": "$screen_view", "attributes": { "$screen": "/settings" } },
    { "id": "018f1e5e-…", "ts": "2026-08-30T10:00:09Z", "name": "subscribed",
      "attributes": { "plan": "pro", "$app_version": "2.5.0" } },
    { "id": "018f1e5f-…", "ts": "2026-08-30T10:00:10Z", "family": "measures",
      "name": "checkout_api", "value": 340, "measure": "time",
      "attributes": { "$sample_rate": 0.1 } }
  ]
}
```

An event is `{id, ts, family, name, value, measure, attributes}`; `family` is
optional for views and product events, and `value` and `measure` belong to
measures only.

### Attribute merge

Batch-level `attributes` are defaults and per-event `attributes` override them
**key by key**. That is the only merge rule, and it applies to system (`$`) and
ordinary keys alike.

A `null` means "not sent": a `null` batch value is ignored, and a `null`
per-event value removes the batch value for that event, so a reserved key
reads as undeclared and a custom key is absent.

### Families and reserved names

| `family` | `name` | Requires | When `family` is omitted |
| --- | --- | --- | --- |
| `views` | `$page_view` or `$screen_view` | `$path` (or `$screen`) | inferred from `name`: these two names are views |
| `product` | anything else | `name` | inferred from `name`: the default for every other name |
| `measures` | any name, or a reserved `$` metric | a `value` from `0` to `1e15` and a `measure` of `time`, `size` or `number` | never inferred — a measure only exists when the event declares `family: "measures"` |

`family` absent keeps the pre-measures rule unchanged: `$page_view` and
`$screen_view` are views, everything else is product, so a client built
before this change keeps working unchanged.

A per-event rejection (the batch's other events are still stored):
- **An unknown `family`** (anything but `views`, `product` or `measures`) is
  rejected, not stored as `product`.
- **A contradiction is rejected:** `family: "views"` with a name that is not
  a view name; `family: "product"` with a view name; `family: "measures"`
  without a valid `value` or `measure`; a reserved metric (`$lcp`, `$inp`,
  `$cls`, `$fcp`, `$ttfb`) sent with the wrong `measure`.

An **unrecognized `$` name is stored as an ordinary custom event or measure**
with a warning, never rejected: a client shipping a future reserved name
(`$session_start`, a future `$tbt` metric) against an older server must not
get a `4xx`, which the retry rules classify as a poison batch to drop. The
same reasoning runs the other way: an **older server ignores `family`, `value`
and `measure`** (unknown top-level fields are dropped by the decoder) and
stores the event as a product event, so a backend or a self-hosted SDK copy
sending measures must upgrade the server first.

### Reserved attribute keys

| Group | Keys | Sent automatically by the JS SDK |
| --- | --- | --- |
| Identity | `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` `$consent` | `$install_id` (identified instance with consent), `$consent` |
| Environment | `$kind` `$platform` `$os` `$os_version` `$os_name` `$browser` `$browser_version` `$device` `$device_model` `$app_version` `$app_locale` `$browser_locale` `$display_width` `$display_height` | `$kind`, `$platform` (`web` for the web kind), `$os` `$os_version` `$os_name` `$browser` `$browser_version` `$browser_locale` `$device` `$display_width` `$display_height` |
| Location | `$host` `$path` `$screen` `$utm_source` `$utm_medium` `$utm_campaign` `$referrer` | `$host` `$path` (web kind) or `$screen` (app kind) on views and, from the last view, on product events; `$referrer` `$utm_source` `$utm_medium` `$utm_campaign` on web views only |
| Sampling | `$sample_rate` | on every Web Vital: the `vitals` rate, `1` included |

`$sample_rate` is a number in `[0.0001, 1]`, read on measures only; each stored
row counts as `1/rate`. Outside that range it is stored as `1` with a warning;
on any other family it is dropped with a warning.

Every key but `$sample_rate` is stored on views and product events alike;
`$sample_rate` is dropped, with a warning, on both and stored on measures
only. The SDK sends the rest only when the page sets them (`identify()`,
`group()`, `attrs()`, the `platform`, `appVersion` and `appLocale` options).
`autoAttributes: false` turns the derived environment and location off,
except what a view needs.

`$consent` is whether the client had consent to keep anything on the device
when it sent the event: `1` (or `true`) given, `0` (or `false`) not given, as a
number, boolean or string in any case. Absent, `null` or `""` mean unknown
(and a per-event `null` overrides a batch value to unknown, as the merge rule
implies). Any other value is stored as unknown with a warning, never rejected.

An **unrecognized `$` key is dropped** with a warning; it is not stored as an
ordinary attribute. `$url` is no longer a reserved key — a client sending it
gets a warning that it was ignored *and* a rejection naming `$path`, so send
`$host` and `$path` instead, campaign parameters as `$utm_*`, and the referrer
as an explicit `$referrer`. Location attributes are stored **verbatim**: no URL
parsing, no normalization, no case folding — the client owns that. The client IP
and User-Agent are never stored; the IP is enriched into a country, the
User-Agent is read only to drop crawlers, and both are discarded.

### Timestamps and idempotency

**`ts`** is RFC 3339, UTC: the client's own clock reading when the event
happened, not at flush time. The server clamps it to `[received − max_event_age,
received + 5 minutes]`, records the server clock separately, and **clamps and
counts, never drops**, out-of-range values; `max_event_age` equals
`RETENTION_EVENTS_RAW_DAYS` (30 days by default), so a clamped event can never
target a day already rolled up. **`id`** is a client-generated UUID (v7
recommended, so ids sort by time) and is what makes at-least-once delivery safe:
the write ignores a duplicate primary key, so a batch retried after a timeout
that actually succeeded is a no-op. **Omit `id` and a replayed batch
double-counts.**

### Responses and retry

```jsonc
202 {
  "accepted": 2,
  "rejected": 1,
  "errors":   [ { "index": 3, "reason": "view requires $path or $screen" } ],
  "warnings": [ { "index": 1, "reason": "unknown reserved key $app_ver, ignored" } ]
}
```

| Code | Meaning | Client action |
| --- | --- | --- |
| 202 | Accepted for processing | Do not retry |
| 400 | Malformed envelope | Drop — poison batch |
| 401 | Unknown or disabled key | Drop |
| 403 | `Origin` present and not allowed | Drop |
| 413 | Body or event count over limit | Split and retry |
| 5xx | Server fault | Retry with backoff |

`errors` and `warnings` are capped at 10 entries each, and rejection is **per
event, not per batch**: one malformed event increments `rejected` and the rest
still lands. **Normative: retry only on 5xx and network failure — any other 4xx
is a poison batch to drop**; the `202` is returned **before** the write, so
`accepted` counts validation, not persistence.

### Limits

| Limit | Value |
| --- | --- |
| Body | 256 KB |
| Events per batch | 500 |
| Attributes per event | 50 |
| Attribute key length | 64 characters |
| Attribute value length | 512 characters (truncated, not rejected) |

### Origin and CORS

A present `Origin` must match the project's `allowed_origins`; an absent one is
accepted, so native apps are unaffected, but Electron and Tauri renderers **do**
send one and need their scheme (`tauri://localhost`, `app://.`, `file://`)
listed. An entry may contain `*`: a bare `*` allows any origin, elsewhere it
stands for any run of characters within the origin, so `https://*.example.com`
matches every subdomain (not the apex, not `http://`) and
`https://app.example.com:*` any port; the matched origin is echoed back, never
`*`. Preflight is answered with `Access-Control-Allow-Headers: Content-Type,
X-Analytics-Key` and echoes the matched origin. Send the key in the body with
`Content-Type: text/plain` to skip preflight entirely, which makes the request
CORS-simple.

---

## Answer questions with the data

A connected session gets thirty-four tools: the eighteen below, and sixteen
that build the dashboards served at `/app/`, which are documented in
`docs://reporting` ([reporting.md](reporting.md)). To build or change a
dashboard, call `reporting_guide` first.

Reach for a purpose-built tool before `query` — they are cheaper, they cannot
be malformed, and they already apply the caveats below. All the reading tools
take `project_id`, `from` and `to` as `YYYY-MM-DD` unless noted.

| Tool | Extra parameters | Returns |
| --- | --- | --- |
| `list_projects` | none | Every project with its `project_id`, name, `archived`, `allowed_origins` and declared `attributes`. Call this first — every other tool needs a `project_id` |
| `views_overview` | `kind` (optional) | Visitors, views, sessions, bounces, average session length per day, summed across kinds unless `kind` filters one |
| `views_breakdown` | `dimension`, `limit` (default 20) | Top rows for one of `kinds`, `paths`, `hosts`, `referrers`, `utm`, `countries`, `platforms`, `os`, `browsers`, `app_versions`, `devices`, `displays`, `consent`, `locales`. Two-key dimensions return both columns. `consent` is `given`, `none` or `unknown`. `locales` pairs `browser_locale` with `app_locale`, either empty when not sent. |
| `product_events` | `event` (optional filter) | Count and unique users per event name, plus daily totals |
| `product_attributes` | `event`, `key` | Count, unique users and unique groups per value of a declared attribute. `$platform`, `$os`, `$app_version`, `$app_locale`, `$kind`, `$browser`, `$device` and `$browser_locale` are always available; a custom key, or one of `$host`, `$path`, `$referrer`, `$utm_source`, `$utm_medium`, `$utm_campaign`, `$os_version`, `$browser_version` and `$device_model`, only appears once the project declares it. `unique_groups` is empty for days rolled up before it was measured and `0` when it was measured and no group was involved |
| `measures` | `name`, `attr_key` | per metric: samples, estimated count, mean, p50/p75/p95 (time in ms, size in bytes) |
| `retention` | `actor` (`user` or `install`) | Cohort curves, plus `aggregated_through` — cohorts after that day are **absent, not zero**. Empty for a project whose clients send neither `$user_id` nor `$install_id` |
| `identities` | `kind` (`user` or `group`), `limit` | Per-user or per-group activity with display names. **Surfaces personal data on projects whose clients send ids** |
| `query` | `sql` | A single read-only `SELECT`/`WITH` against the views. Row-capped and time-limited; `meta` and SQLite's internal tables (`sqlite_master`, `dbstat`, …) are refused, whether named directly or as a quoted or single-quoted string. Non-ASCII names must be quoted |

**Managing** — `create_project`, `update_project`, `archive_project`,
`restore_project`, `issue_ingest_key`, `list_ingest_keys`, `enable_ingest_key`,
`disable_ingest_key` and `integration_guide`, all described in [Set up a
project](#set-up-a-project).

**Resources:** `docs://twillingate` (this document), `docs://deployment`
(installing and configuring the collector), `docs://reporting` (building
dashboards), `schema://views` (the authoritative column list — read it before
writing SQL), `schema://projects` (the live registry) and the reporting
snapshots `schema://components`, `schema://dashboards` and
`schema://widgets`. Enabling the endpoint, choosing an auth mode and pointing
a client at it are in [deployment.md](deployment.md#the-api-endpoint).

### HTTP API

Every tool above except `integration_guide` is also a REST route under `/api/`,
guarded by the same bearer token (`Authorization: Bearer …`) as MCP. Send and
receive JSON. `integration_guide`, `reporting_guide` and the `docs://`
resources are MCP-only. The dashboard routes are listed in
[reporting.md](reporting.md#http-api).

Every route is also described by an OpenAPI 3.1 document at
`/api/openapi.json`, generated from the same definitions that register the
routes, with each operation's `operationId` naming the tool it mirrors.
`/api/docs` renders it in Swagger UI; signed in to the dashboards, Try it
out uses that login. Neither needs a
token: they describe routes and fields, never data.

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://t.example.com/api/projects/1/views/overview?from=2026-09-01&to=2026-09-13"
```

| Method | Path | Mirrors | Input |
|---|---|---|---|
| `GET` | `/api/projects` | `list_projects` | — |
| `POST` | `/api/projects` | `create_project` | body: `name`, `allowed_origins`, `attributes`, `skip_key` → 201 |
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
| `GET` | `/api/projects/{project_id}/measures` | `measures` | query: `from`, `to`, `name`, `attr_key` |
| `GET` | `/api/projects/{project_id}/retention` | `retention` | query: `from`, `to`, `actor` |
| `GET` | `/api/projects/{project_id}/identities` | `identities` | query: `from`, `to`, `kind`, `limit` |
| `POST` | `/api/query` | `query` | body: `sql` |
| `GET` | `/api/schema/views` | `schema://views` | — (text/plain) |

Success is the tool's output as JSON, at 200 (201 where noted). Errors are
`{"error":{"code":"…","message":"…"}}`: `invalid` is 400, `not_found` is 404,
`conflict` is 409, anything else is `internal` at 500, and a missing or bad
token is 401. An unknown query parameter, an unknown field in a JSON body, a
query string that does not parse (a bad `%` escape, a bare `;`) and a parameter
given twice are all 400: a filter is never dropped silently.

### Writing SQL against the views

The `query` tool takes read-only SQL against the views below; it is
row-capped (`API_QUERY_MAX_ROWS`, default 1000) and time-limited
(`API_QUERY_TIMEOUT`, default `10s`). Read `schema://views` for the
authoritative column list; it is kept in step with the migrations and
carries the caveats the DDL cannot express. Three matter most:

1. `day` columns are TEXT `'YYYY-MM-DD'` (UTC). Compare and `BETWEEN` as
   strings.
2. Every `v_*` view includes yesterday and today: each stitches aggregated
   history (`agg_*` tables) to a live half computed from raw rows.
3. **`v_retention` has no live half.** It refreshes at the 03:00 UTC daily pass;
   cohort days after that are ABSENT, not zero. It holds cohorts only for
   actors identified by `$user_id` or `$install_id`; a project whose clients
   send neither has none. Each actor is cohorted by how it was
   identified, `user` (sent a `$user_id`) or `install` (a stable
   `$install_id`); a connection-hash actor is not cohorted. On a client that
   mints an install id per page load the `install` curve reads near zero — read
   the `user` curve. Cohorts counted before migration 011 have no `user` rows
   and sit wholly under `install`.

Every view carries a `project_id` column — always filter on it; the ids are
the ones `list_projects` returns. The views family
is `v_views_daily` (per kind), `v_views_paths`, `v_views_hosts`,
`v_views_referrers`, `v_views_utm`, `v_views_countries`, `v_views_platforms`,
`v_views_os`, `v_views_browsers`, `v_views_app_versions` (keyed by `platform`
and `app_version`), `v_views_devices`, `v_views_displays`, `v_views_consent`
(`given`, `none` or `unknown`, where `unknown` is every view stored before
migration 018 or sent without `$consent`) and `v_views_locales` (keyed by
`browser_locale` and `app_locale`, `''` where one was not sent; a view sending
neither is left out, and no day rolled up before migration 019 has rows);
every other dimension is
capped at 500 values per day, the tail is one `(other)` row whose visitors
are distinct actors, not a sum, and `consent` never reaches it — it only ever
has three values. `os`, `browser` and `device` are lower-case
closed vocabularies (see [Declaring the
environment](#declaring-the-environment)) where `other` is a real value outside
the list and `(other)` is the cap. Product events have `v_product_daily`,
`v_product_totals` and `v_product_attrs` (whose `unique_groups` is NULL, not
zero, for days rolled up before it was measured — `MAX()` skips it, `SUM()`
would too, a `COALESCE` to 0 would lie). Measures (performance timings, sizes
and counts, including Web Vitals) have `raw_measures`, `v_measures_daily`
and `v_measures_attrs` (keyed the same way as `v_product_daily` /
`v_product_attrs`, plus `measure` — `time`, `size` or `number` — and
`bucket`/`approx_value` from the log-scale histogram; the `measures` tool
does the percentile math over them). `raw_views`, `raw_product`,
`raw_measures` and `v_events_flat` are all views over the one raw table, so
each now carries every family's columns: `value`, `measure` and
`sample_rate` are NULL, `''` and `1` on `raw_views`/`raw_product` rows (they
only mean something on a measure), and `raw_*` additionally carries `bucket`,
the log-scale bucket `value` falls in (NULL outside measures). `v_events_flat`
holds every raw row of every family (views, product events and measures)
with every typed column of the raw row: its `family` column — filter
`family = 'product'` for product events alone, `family = 'measures'` for
measures — `kind`, the identity, location and environment columns (`path`,
`os`, `country`, …), its `consent` column (1, 0 or NULL), the raw
`attributes` JSON, `value`, `measure`, `sample_rate`, and one `attr_*` column
per declared custom attribute.
`v_identity_daily` and `identities` join user and group activity to display
names; `v_identity_daily` keeps the busiest 500 users and 500 groups per day
and drops the rest with no `(other)` row, so do not sum it for totals.
`v_retention` is keyed by `actor_kind`.

Cost note: the views' live halves sessionize raw rows with window functions, and
a `WHERE` on `day` may not prune that work. Narrow ranges and the `agg_*` tables
are cheaper.
