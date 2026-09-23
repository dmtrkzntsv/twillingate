# One global, a factory, and a friendlier SDK

Status: draft
Date: 2026-09-22

## Sequencing

First of two specs. This one changes the SDK, the served bundle and the
contract page, and nothing on the server. The second removes the project's
server-side identity mode and makes the collector store what it is sent:

1. **this spec** — SDK and docs only
2. `sdk-identity-on-the-client` (to be written) — drops `projects.identity`,
   migration 017, the tool and CLI fields, the guide's mode paragraphs and
   the README's GDPR section

The order is forced by one fact. Today an anonymous site may call
`identify()` and the server hashes the id with a daily salt. If the server
stopped hashing before the SDK stopped sending, every id sent in the window
between the two releases would be stored raw. So the SDK stops sending
first; until the second spec lands the server's mode is redundant, which is
harmless.

## Problem

The SDK works, and it is awkward to hold in the head. Four things, each
small, add up:

1. **A second project on the page is a second global.** `data-instance="et"`
   registers `window.et`; a bundled consumer passes `instance: "et"` to
   `init()` and gets a storage prefix but nothing to look the instance up
   by. There is no one place that knows which instances exist, and "which
   global do I call" is a question the docs answer with a table.
2. **The identity mode is set twice.** The project carries `identity`
   server-side and the tag repeats it in `data-identity`. The client copy
   only decides what persists under consent, the two must agree by hand,
   and when they disagree the client one quietly wins for what is on the
   device.
3. **Calls before `init()` are dropped with a warning.** A bundled app that
   registers a listener or identifies the user before its own `init()`
   loses the call. A page listener registered after the entry pageview
   cannot affect it, and the remedy is a placement rule.
4. **The API has grown by accretion.** `page()` is overloaded four ways and
   passing a function means "register a listener". `attrs()` defaults lose
   to values the SDK derives, so suppressing `$referrer` for a
   programmatic instance needs `$referrer: null` on every call. There is no
   hook for product events, opt-out is a localStorage key typed by hand,
   and the only way to see what the SDK sends is the network tab.

A survey of the SDKs developers rate well — PostHog, Segment, Plausible,
Umami, Amplitude, Mixpanel, Fathom, Vercel — shows what they share and we
lack: a hook on every event, named instances behind one global, debug
logging, an opt-out call, pluggable storage, and declarative event tagging
in markup.

## What does not change

The wire format, every reserved attribute, the server, the project
registry, the `Snippet()` output and the integration guide's tag. Storage
keys keep their names (`twillingate_*` for the default instance,
`<name>_*` for the rest) and `twillingate_ignore` stays global. Consent
keeps its meaning and its default. Detection, masking, routing modes, the
retry queue and its bounds, batching and `sendBeacon` on unload are
untouched. The Plausible shim needs nothing: it calls
`window.twillingate.track`, which still exists.

## Design

### 1. One global that is both the default instance and the factory

`window.twillingate` is one object with two roles. It is the default
instance, so `init`, `page`, `track`, `identify`, `consent`, `detectOS`,
`util` and `VERSION` stay where they are. It also owns a registry of named
instances:

- `create(name, opts?)` validates `name` against the existing
  `^[a-z][a-z0-9_]{0,15}$` rule, constructs an instance whose storage keys
  are prefixed with the name, calls `init(opts)` when options are given,
  registers it and returns it. A name already in the registry returns the
  existing instance with a warning and ignores the new options, so a tag
  and a script that both say `et` never fight. The default name
  `twillingate` is refused, because that instance is the global itself.
- `get(name?)` returns the named instance or `undefined`; with no argument,
  the default instance. Nothing is created on lookup.

```js
twillingate.init({ key: "ak_web…" });                 // the default instance
const et = twillingate.create("et", { key: "ak_app…" });
et.track("budget_created");
twillingate.get("et").track("export");                // from any later script
```

The bundle entry decides what to do from what it finds at
`window.twillingate`:

| Found | Action |
| --- | --- |
| nothing | register the object, then auto-init from the tag if it has `data-key` |
| a live SDK from an earlier tag | do not build a second global. With `data-instance="et"`, hand this tag's attributes to the existing object's `create("et", …)` and stop. Without it, today's duplicate rule: same key or no key stands down with a warning; a different key takes over the default instance with a warning |
| something foreign | warn, leave it alone, run this tag's instance unregistered so the page is still tracked |

`data-instance="et"` therefore means "register me as
`twillingate.get("et")`". No `window.et` is ever created. A tag with
`data-instance` and no `data-key` registers a dormant instance for
`twillingate.get("et").init(opts)` later. The `instance` option is removed
from `init()`; the name is `create()`'s first argument.

A bundled consumer imports the same object from `sdk/src/twillingate.ts`,
so the API is identical whether code reaches it through `window` or an
import.

### 1a. Browser hooks are installed once and fanned out

Today every instance patches `history.pushState` itself and the patches
chain, and every instance adds its own `popstate`, `hashchange`, `online`,
`pagehide` and `visibilitychange` listeners. With a registry there is one
owner for all of that: the bundle's runtime, reached through the global.

The runtime installs each browser hook once, lazily, the first time an
instance is registered, and dispatches to every instance it knows about:

| Browser event | Dispatched as | What each instance decides for itself |
| --- | --- | --- |
| `pushState` (patched once), `popstate`, `hashchange` | navigation | `autoPageviews` off ignores it; `routing: "history"` ignores `hashchange`; dedup against its own last page; its own mask, `onPage` and `onEvent` listeners, `kind` (`$page_view` or `$screen_view`) |
| `online` | back online | replay its own retry queue |
| `pagehide`, `visibilitychange` to hidden | unloading | flush its own queue through `sendBeacon` |
| click, `auxclick`, `submit` in the capture phase | tagged element | `taggedEvents` off ignores it; otherwise `track(name, { path })` through its own listeners |

The instance keeps the decision, the runtime keeps the subscription. Two
projects on one page therefore see one `pushState` patch, not two, and a
page with the web tag and Econumo's product instance gets one navigation
event handed to both, each applying its own configuration: the web
instance emits an automatic `$page_view` with full acquisition data, the
product instance has `autoPageviews: false` and does nothing.

Ownership follows the bundle copy, not the global. A second tag whose copy
hands its attributes to the first copy's `create()` gets an instance that
belongs to the first copy's runtime. An instance that could not be
registered because the global is foreign belongs to its own copy's
runtime, which installs its own hooks; that is the one case with two sets,
and it already costs a warning.

### 2. Every call is legal before `init()`

An instance accepts every call from the moment it exists. Configuration
takes effect immediately: `onPage`, `onEvent`, `attrs`, `identify`,
`group`, `consent`, `debug`, `optOut`. Calls that produce events — `page`,
`screen`, `track`, `flush` — are held in order and run right after
`init()` completes. When automatic pageviews are on, the entry pageview
fires first, then the held calls in the order they were made.

Identity precedence at `init()`: an explicit `user` or `group` option wins
over an earlier `identify()` or `group()`; otherwise the earlier call
stands; only when neither is set does a stored value load.

The hold is capped at the batch size (500); past that the oldest call is
dropped and one warning is logged, so a page that never initialises cannot
leak. A second `init()` on a live instance warns and is ignored, as today.

There is no inline pre-load stub. In snippet mode with a key the entry
pageview fires when the tag executes, so a page listener registered from a
later inline module still gets the "registered after the first pageview"
warning, and `data-mask-url` — resolved inside `init()` — remains the way
to cover the entry page. That advice stays in the docs unchanged.

### 3. The tag's identity mode is the enforcement point

`identity` stays an option and `data-identity` stays an attribute, default
`anonymous`, but it now decides what the instance *sends*, not only what
it stores:

- `anonymous`: the instance never sends `$user_id`, `$user_name` or
  `$install_id`. `identify()` warns once and does nothing; an `installId`
  option is ignored with a warning; no visitor id is ever minted, with or
  without consent. `group()` works and `$group_id`/`$group_name` are sent,
  since a group names an organisation. Consent unlocks only the retry
  queue.
- `identified`: sends the ids it holds. With consent it persists the
  visitor id, user and group; without consent nothing persists and a
  signed-out visitor falls back to the server's daily connection hash.

One behaviour change to name: an anonymous site that called `identify()`
today got user-accurate daily uniques through the salted hash. Afterwards
it gets the connection hash. That is what `anonymous` means.

### 4. Precedence, and a hook on every event

Every event's attributes are assembled in one order:

1. values the SDK derives — `$host`, `$path`, `$referrer`, campaign
   parameters, display size;
2. `attrs()` defaults;
3. the call's own attributes;
4. what listeners return, in registration order: `onPage` listeners for a
   pageview, then `onEvent` listeners for every event.

Later layers override earlier ones. A `null` or `undefined` value drops
the key after the last layer. Configured beats guessed, explicit beats
configured. Today `attrs()` defaults sit *under* derived values, so a
default `$referrer: null` loses to the real referrer; after this change
one `attrs({ $referrer: null })` covers every pageview of a programmatic
instance.

`onPage(fn)` replaces `page(fn)`. `page()` keeps its three data forms.
`page(fn)` still works for this release, forwards to `onPage` and warns
that it is deprecated. The listener contract — threaded `host` and `path`,
`false` cancels, post-mask `url` — is unchanged.

`onEvent(fn)` is new and runs for every event, after `onPage` has finished
with a pageview. It receives `{ name, attributes }` and returns attributes
to merge, `false` to drop the event, anything else to observe. It is the
`before_send` every surveyed SDK has.

A listener that throws, in either hook, drops the event with a warning.
Masking already fails closed; listeners now do too.

### 5. Storage drivers

`storage` selects where the instance keeps its keys once consent allows:

| Value | Meaning |
| --- | --- |
| `"localStorage"` (default) | today's behaviour |
| `"sessionStorage"` | identity and queue live for the tab |
| `"memory"` | nothing on the device; equivalent to no consent |
| `"cookie"` | host-only cookie per key, `SameSite=Lax`, one year, `Secure` on https; identity keys only — a retry queue in a cookie would ride on every request, so the queue stays in memory |
| `{ get(key), set(key, value), remove(key) }` | a custom driver |

Consent stays the gate: without it no driver is touched, with it the
driver holds the keys. A driver that throws counts as unavailable, exactly
as localStorage does today. There is no attribute: a tag keeps
localStorage, and a site that wants another driver calls `init()` from
code, the same rule `url` and `flushInterval` follow. The docs show a six-line custom driver for a cookie on a shared parent domain,
which is the one thing the built-in cookie driver does not do.

`twillingate_ignore` stays in localStorage regardless of driver: opting out
is about the person, not one instance.

### 6. Opt-out, debug

`optOut` is an option: `true`, or a function consulted at every event and
never cached, OR-ed with the default `twillingate_ignore` check.
`init({ optOut: () => location.hostname === "localhost" })` is the
code-side domain filter. There is no attribute.

`optOut(flag?)` is a method: a boolean writes or clears
`twillingate_ignore`; the call returns the effective state, callback
included.

Debug logging is turned on three ways, and there is no attribute for it:
the `debug` option in code, the `debug(flag?)` method, or
`localStorage.twillingate_debug = "true"` typed in the console. The flag is
global and unprefixed like `twillingate_ignore`, because debugging is about
the page, not one instance, and it is read at every log decision so a
reload keeps it. `debug(true)` writes the flag and `debug(false)` clears
it, so the console toggle survives navigation; the call returns the
effective state. Logging prints each emitted event and each send with its
outcome, prefixed with the instance name (`[twillingate]`,
`[twillingate:et]`), and never changes what is sent.

### 7. Tagged elements

Any element with `data-twillingate-event="signup"` tracks that event on
every registered instance that has tagged events on: on click with the
main or middle button, or, for a `<form>`, on submit. The runtime listens
once (§1a) and each instance filters. The nearest tagged ancestor of the
click target wins. `path` is always added as an attribute — a nav CTA would fire
identically from every page otherwise, the same reasoning the Plausible
shim records. No other per-element attribute is read: the event carries
its name and `path`, nothing more. The listeners
run in the capture phase so a handler that stops propagation cannot eat
the event. On by default for every instance, the same rule as
`autoPageviews`; `taggedEvents: false` opts an instance out, which a
programmatic product instance such as Econumo's will normally do. There is
no attribute for it: a tag always has them on. The Plausible shim stays
for Plausible-class markup.

### 8. The rest of the option review

- `autoPageviews` defaults to `true` in `init()` and `create()`, matching
  the tag; `false` opts out. In a runtime without `location` and
  `history` it is a no-op.
- `init()` and `create()` return the instance.
- `flush()` loses its public `unloading` parameter; the unload path calls
  a private method.

### 9. How Econumo is wired afterwards

The web tag is whatever liltag injects. Econumo's own code creates a
second instance from it; no second tag, no second global.

```html
<script defer src="https://t.example.com/js/twillingate.js" data-key="ak_web…"></script>
```

```js
const et = twillingate.create("econumo", {
  key: "ak_econumo…",
  identity: "identified",   // sends $user_id / $group_id; nothing persists without consent
  autoPageviews: false,     // views come from the router below
  taggedEvents: false,      // markup CTAs belong to the web tag
  user: userHash,           // or et.identify(userHash) after login
  group: workspaceId,
});
et.attrs({ $host: "selfhosted_ab12", $referrer: null });   // self-hosted: hashed host, no referrer
router.afterEach((to) => et.page(to.path));                 // no campaign parameters: explicit path
et.track("budget_created", { currency: "EUR" });
et.reset();                                                 // on logout
```

An explicit path is resolved against the page URL but carries only what
is passed, so campaign parameters are never read; that already holds.

### 10. Tag installation, consolidated

Everything the tag does, in one place. Nothing here is new; it is §1, §1a,
§3 and §7 read from the tag's side.

**The snippet** is unchanged and stays one `<script>` element:

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…" data-identity="anonymous"></script>
```

**Attributes.** No row is added, two are removed (`data-user`,
`data-group`), two change meaning.

| Attribute | `init()` option | Meaning after this spec |
| --- | --- | --- |
| `data-key` | `key` | Required to auto-init. Without it the tag loads dormant for `init()` from code. |
| `data-identity` | `identity` | `anonymous` (default) or `identified`. **Now the enforcement point:** anonymous never sends `$user_id`, `$user_name` or `$install_id`; identified sends them and, with consent, persists visitor id, user and group. |
| `data-auto="off"` | `autoPageviews` | Disable automatic pageviews. Both default on. |
| `data-mask-url` | `maskUrl` | Unchanged. |
| `data-routing` | `routing` | Unchanged. |
| `data-kind` | `kind` | Unchanged. |
| `data-consent` | `consent` | Unchanged. |
| `data-instance` | `create(name)` | **Now:** register this tag's instance as `twillingate.get(name)`. No `window.<name>`. The one attribute without an `init()` field. |

Code-only, deliberately: `url`, `user`, `group`, `installId`,
`flushInterval`, the environment overrides, `storage`, `taggedEvents`,
`optOut`, `debug`. Identity is a fact the application knows, so it is set
from code — `user` and `group` in `init()` or `create()`, or `identify()`
and `group()` after login — never pasted into markup. A tag keeps
localStorage, always has tagged events on, and is debugged and opted
out through the two localStorage flags below.

**What happens when the tag executes.** The script is `defer`, so it runs
after the document is parsed, in document order with other deferred
scripts.

1. Look at `window.twillingate`.
   - Nothing there: register the global (default instance plus factory).
   - A live SDK from an earlier tag: do not build a second global. With
     `data-instance`, call the existing `create(name, attributes)` and
     stop. Without it, same key or no key stands down with a warning; a
     different key takes over the default instance with a warning.
   - Something foreign: warn, leave it, run this tag's instance
     unregistered.
2. Registering the first instance installs the runtime's browser hooks
   once: the `pushState` patch, `popstate`, `hashchange`, `online`,
   `pagehide`, `visibilitychange`, and the capture-phase click, `auxclick`
   and `submit` listeners for tagged elements. Later instances subscribe;
   nothing is installed twice.
3. With `data-key`, `init()` runs from the attributes. Automatic
   pageviews are on unless `data-auto="off"`, so the entry pageview is
   emitted synchronously here, after the mask is resolved. Calls the
   page made on the instance before this point (a dormant `data-instance`
   tag initialised later from code) run now, entry pageview first.
4. From here the instance receives every navigation, online, unload and
   tagged-element event through the runtime and applies its own settings.

**Two tags.** The second tag's copy of the bundle finds the first's global
and hands over: with `data-instance="et"` its attributes become
`twillingate.create("et", …)` in the first copy's registry; without it the
duplicate rule applies. One set of browser hooks serves both.

**Inline code and the tag.** There is no pre-load stub. Code that calls
`twillingate` must run after the tag has executed: an inline
`<script type="module">` placed after the tag, or any code after
`DOMContentLoaded`. A page listener that must cover the entry pageview
uses `data-mask-url`, which is resolved before it fires.

**Person-level flags**, both in localStorage, both unprefixed, both read
live: `twillingate_ignore = "true"` opts the device out of every instance
(`optOut(true)` writes it); `twillingate_debug = "true"` logs every event
and send of every instance (`debug(true)` writes it).

### 11. Use from code, consolidated

Everything the SDK offers to code, in one place. Nothing here is new
except the loader example.

**Three ways to get the object.** All three yield the same API.

1. *The tag, dormant.* Load the file without `data-key` and use
   `window.twillingate` after it has executed. `url` defaults to the
   script's origin.
2. *Inject the script from code.* The same file, appended by the
   application when it decides analytics should run — after a consent
   check, after login, or only in production. A dynamically inserted
   classic script still sets `document.currentScript`, so the origin
   default and every attribute work exactly as for a pasted tag.

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
   tg.init({ key: "ak_web…", consent: () => cmp.hasConsent("analytics") });
   ```

   Setting `s.dataset.key = "ak_web…"` before appending makes the injected
   script auto-init on load instead, exactly as a pasted tag would, and
   any other `data-*` attribute can be set the same way. If the script is
   blocked, `onerror` fires and the page has no `window.twillingate`; a
   caller that wants to keep calling regardless guards with
   `window.twillingate?.track(...)`.
3. *Bundle the module.* `import twillingate from "…/sdk/src/twillingate"`
   gives the same object without registering a global. Bundling and a
   tag on the same page means two bundle copies, each with its own
   runtime and hooks; when a tag is on the page, prefer reaching the
   global. `url` is required here, since there is no script to read an
   origin from.

**Options.** `init(opts)` on any instance, `create(name, opts)` for a
named one.

```ts
interface InitOptions {
  key: string;                                  // required
  url?: string;                                 // default: the loading script's origin
  identity?: "anonymous" | "identified";        // default "anonymous"; decides what is SENT (§3)
  user?: string;                                // identity known at init; else identify() later
  group?: string;
  consent?: boolean | string | (() => unknown); // default false; a name is read on window live
  storage?: "localStorage" | "sessionStorage" | "memory" | "cookie" | StorageDriver;
                                                // default "localStorage"; used only with consent
  kind?: string;                                // default "web"
  platform?: string;                            // default "web" while kind is "web"
  os?: string; osVersion?: string; osName?: string;
  browser?: string; browserVersion?: string; device?: string;
  appVersion?: string;
  installId?: string;                           // ignored, with a warning, on an anonymous instance
  autoPageviews?: boolean;                      // default true
  taggedEvents?: boolean;                       // default true
  maskUrl?: MaskSpec;                           // resolved before the entry pageview
  routing?: "history" | "hash";                 // default "history"
  flushInterval?: number;                       // default 1000 ms
  optOut?: boolean | (() => unknown);           // OR-ed with the twillingate_ignore flag
  debug?: boolean;                              // OR-ed with the twillingate_debug flag
}

interface StorageDriver {
  get(key: string): string | null;
  set(key: string, value: string): void;
  remove(key: string): void;
}
```

**Methods on every instance.** Every one may be called before `init()`
(§2). Those marked *held* run after `init()`, in order.

| Call | Returns | Meaning |
| --- | --- | --- |
| `init(opts)` | the instance | Configure and start. A second call warns and is ignored. |
| `page()` · `page(path, attrs?)` · `page(attrs)` | — | `$page_view` (or `$screen_view` for a non-web `kind`). *Held.* An explicit path carries no campaign parameters. |
| `screen(name, attrs?)` | — | Explicit `$screen_view`. *Held.* |
| `track(name, attrs?)` | — | Product event. *Held.* |
| `attrs(defaults)` · `attrs(null)` | — | Default attributes under every event; merge on repeat, `null` clears. They override derived values (§4). |
| `identify(user, name?)` | — | `$user_id`, `$user_name`. Inert on an anonymous instance. |
| `group(id, name?)` | — | `$group_id`, `$group_name`. Every mode. |
| `reset()` | — | Logout: clears user, group, visitor id and the retry queue. |
| `flush()` | — | Send the queue now. *Held.* |
| `consent(granted?)` | boolean | Pin, hand back (`null`), or read effective consent. |
| `optOut(flag?)` | boolean | Write or clear `twillingate_ignore`; returns the effective state, callback included. |
| `debug(flag?)` | boolean | Write or clear `twillingate_debug`; returns the effective state. |
| `onPage(fn)` | — | Pageview listener: threaded `host`/`path`, return attributes, `false` cancels. Replaces `page(fn)`. |
| `onEvent(fn)` | — | Every event: `{ name, attributes }` in, attributes or `false` out. |
| `detectOS(s?)` · `detectBrowser(s?)` · `detectDevice(s?)` | info | Pure detection, unchanged. |
| `util.maskIds` · `util.withQuery` | string | Path helpers, unchanged. |

**On the global only:** `create(name, opts?)`, `get(name?)`, `VERSION`.

**Listener contracts.**

```ts
type PageListener  = (p: { url: string; host: string; path: string; referrer: string;
                           attributes: Record<string, unknown> }) =>
                     Record<string, unknown> | false | void;
type EventListener = (e: { name: string; attributes: Record<string, unknown> }) =>
                     Record<string, unknown> | false | void;
```

Order for a pageview: derived values, `attrs()` defaults, the call's
attributes, then `onPage` listeners, then `onEvent` listeners; `null`
drops a key at the end; a throwing listener drops the event with a
warning.

**Worked examples.**

*A consent manager and a shared-domain cookie:*

```js
const cookies = {
  get: (k) => document.cookie.match(new RegExp("(?:^|; )" + k + "=([^;]*)"))?.[1] ?? null,
  set: (k, v) => { document.cookie = `${k}=${v}; domain=.example.com; path=/; max-age=31536000; SameSite=Lax; Secure`; },
  remove: (k) => { document.cookie = `${k}=; domain=.example.com; path=/; max-age=0`; },
};
twillingate.init({
  key: "ak_web…",
  identity: "identified",
  consent: () => cmp.hasConsent("analytics"),   // read at every decision, never cached
  storage: cookies,                             // visitor id, user, group; the queue stays in memory
});
```

*Keeping development traffic out, from code:*

```js
twillingate.init({ key: "ak_web…", optOut: () => location.hostname === "localhost" });
```

*Shaping every event of one instance:*

```js
const et = twillingate.create("econumo", { key: "ak_econumo…", identity: "identified",
                                           autoPageviews: false, taggedEvents: false });
et.attrs({ $host: "selfhosted_ab12", $referrer: null });   // defaults beat derived values
et.onEvent(({ name, attributes }) => name.startsWith("debug_") ? false : { app: "econumo" });
et.onPage(({ path }) => ({ $path: path.replace(/\/budgets\/\d+/, "/budgets/[id]") }));
router.afterEach((to) => et.page(to.path));
```

*Verifying from the console, on any page:*

```js
localStorage.twillingate_debug = "true";   // or twillingate.debug(true)
// [twillingate] $page_view { $host: "shop.example.com", $path: "/account/[id]" }
// [twillingate] sent 1 event → 202
// [twillingate:econumo] budget_created { currency: "EUR", app: "econumo" }
```

## Documentation

`docs/twillingate.md`, in the same commit as the SDK, per CLAUDE.md:

- the snippet table: `data-instance` now means "register under
  `twillingate.get(name)`"; the `data-user`/`data-group` row is removed;
  no new rows; the parity sentence names the one exception,
  `data-instance`, which maps to `create()`'s name, and the code-only
  list grows by `user`, `group`, `storage`, `taggedEvents`, `optOut` and
  `debug`;
- the SDK-only example: no `instance`, `autoPageviews` on by default,
  `debug`, `storage`, `optOut`, `taggedEvents`;
- the runtime API list: `onPage`, `onEvent`, `create`, `get`, `optOut`,
  `debug`, `init` returning the instance, the precedence rule;
- "Consent and storage": an anonymous instance sends no identifier at all;
  the drivers table; the custom cookie-driver example;
- "Two tags on one page" rewritten around `create` and `get`, and the
  sentence about chained `pushState` patches replaced by the one-hook
  rule;
- "Listeners": `onPage` and `onEvent`, the fail-closed rule;
- a "Tagged elements" subsection;
- "Identity": one sentence saying the tag's mode decides what is sent; the
  server table stays until the second spec;
- "Privacy behaviour": `optOut()` and `twillingate_debug`.

`README.md`'s SDK example gains nothing it does not already show; its
GDPR section is the second spec's job. `sdk/README.md` is unchanged.

## Tests

SDK suite (vitest), one `describe` each:

- the registry: `create`, `get`, duplicate name, invalid name, default
  name refused, `create` without options is dormant;
- the entry's three decisions; a `data-instance` tag landing in the
  registry; a second bundle copy joining the first's registry; the
  duplicate rule still standing down;
- the runtime: `history.pushState` patched once with two instances
  registered; one navigation reaching both, one with `autoPageviews` off
  emitting nothing; `hashchange` reaching only the hash-routed instance;
  `online` replaying each queue; unload flushing each queue; hooks
  installed lazily on the first registration and never twice;
- holding before `init()`: order preserved, entry pageview first, cap and
  warning, identity precedence against `init` options and storage;
- anonymous sends no `$user_id`, `$user_name` or `$install_id`,
  `identify()` inert with one warning, `installId` ignored, groups still
  sent; identified persists with consent;
- precedence: derived < defaults < call < listeners; `null` drops;
- `onEvent` on a product event and a pageview; `onPage` deprecation
  through `page(fn)`; a throwing listener drops the event;
- drivers: each built-in, a custom one, a throwing one, cookie skipping
  the queue;
- opt-out: the flag, a callback, the method's return value;
- `debug` output, the runtime toggle writing `twillingate_debug`, and the
  flag set by hand before load;
- tagged elements: click, middle click, submit, ancestor, `path`, one
  listener set feeding two instances, the off switch on one of them,
  nothing but the name read;
- `autoPageviews` default in code; `init()` returning the instance.

Go:

- `internal/api/docs_sync_test.go` SDK symbols gain `onPage`, `onEvent`,
  `create`, `get`, `optOut`, `debug`, `storage`, `taggedEvents`,
  `twillingate_debug` and lose `data-user` and `data-group`;
- `internal/server/twillingate_script_test.go` markers gain
  `twillingate_debug` and `data-twillingate-event`;
- the SDK's tag-to-option parity test gets one named exception,
  `data-instance`, which maps to `create()`'s name.

## Breaking changes

Commit as `feat(sdk)!`. The release note lists:

- `window.<name>` globals from `data-instance` are gone; use
  `twillingate.get(name)`;
- `data-user` and `data-group` are gone; set identity from code with the
  `user` and `group` options or `identify()` and `group()`;
- the `instance` option is gone; use `twillingate.create(name, opts)`;
- an anonymous instance no longer sends `$user_id`, `$user_name` or
  `$install_id`, and `identify()` is inert on it;
- `autoPageviews` is on by default in `init()`;
- `attrs()` defaults now override SDK-derived values;
- `page(fn)` is deprecated in favour of `onPage(fn)` and will be removed;
- tagged elements are on by default: markup carrying
  `data-twillingate-event` starts tracking.

## Out of scope

- The server half: dropping the project mode, migration 017, tools, CLI,
  guide and README. The second spec.
- An inline pre-load stub. Considered and declined; `init()`-time holding
  covers bundled apps, and the entry pageview keeps its placement rule.
- A domain allowlist attribute. Declined in favour of the `optOut`
  callback.
- Per-element attribute values on tagged elements
  (`data-twillingate-<attr>`). Declined; the event carries its name and
  `path`.
- Publishing an npm package, framework components, renaming `data-auto`,
  cookies on a shared parent domain as a built-in, Do Not Track, click
  autocapture, pageleave and scroll depth (server columns), promise
  returning calls, client-side rate limiting.
