/* One owner for every browser hook.
 *
 * Every instance used to patch history.pushState itself and add its own
 * popstate, hashchange, online, pagehide and visibilitychange listeners;
 * two instances meant two patches chained on one another. The runtime
 * installs each hook once, lazily, when the first instance subscribes,
 * and dispatches to every subscriber. The instance keeps the decision
 * (autoPageviews off, routing mode, taggedEvents off), the runtime keeps
 * the subscription. There is one runtime per bundle copy.
 */

export type NavigationSource = "push" | "pop" | "hash";

export interface Subscriber {
  onNavigate(source: NavigationSource): void;
  onOnline(): void;
  onUnload(): void;
  onTagged(name: string, path: string): void;
}

const TAG_ATTR = "data-twillingate-event";

type Listener = [EventTarget, string, EventListener, boolean];

class Runtime {
  private subs: Subscriber[] = [];
  private hooked = false;
  private nativePushState: History["pushState"] | null = null;
  private listeners: Listener[] = [];

  subscribe(s: Subscriber): void {
    if (this.subs.indexOf(s) < 0) this.subs.push(s);
    this.install();
  }

  unsubscribe(s: Subscriber): void {
    this.subs = this.subs.filter((x) => x !== s);
  }

  /** Test hooks. */
  reset(): void {
    for (const [target, type, fn, capture] of this.listeners) target.removeEventListener(type, fn, capture);
    this.listeners = [];
    if (this.nativePushState && typeof history !== "undefined") history.pushState = this.nativePushState;
    this.nativePushState = null;
    this.subs = [];
    this.hooked = false;
  }

  installed(): boolean {
    return this.hooked;
  }

  subscribers(): number {
    return this.subs.length;
  }

  private each(fn: (s: Subscriber) => void): void {
    for (const s of this.subs.slice()) {
      try {
        fn(s);
      } catch (e) {
        console.warn("twillingate: an instance threw while handling a browser event", e);
      }
    }
  }

  private on(target: EventTarget, type: string, fn: EventListener, capture = false): void {
    target.addEventListener(type, fn, capture);
    this.listeners.push([target, type, fn, capture]);
  }

  private install(): void {
    if (this.hooked || typeof window === "undefined" || typeof document === "undefined") return;
    this.hooked = true;
    if (typeof history !== "undefined") {
      const native = history.pushState;
      this.nativePushState = native;
      history.pushState = (...args: Parameters<History["pushState"]>) => {
        native.apply(history, args);
        this.each((s) => s.onNavigate("push"));
      };
      this.on(window, "popstate", () => this.each((s) => s.onNavigate("pop")));
      this.on(window, "hashchange", () => this.each((s) => s.onNavigate("hash")));
    }
    this.on(window, "online", () => this.each((s) => s.onOnline()));
    // pagehide covers navigations and tab closes; visibilitychange the
    // mobile cases where pagehide never fires.
    this.on(window, "pagehide", () => this.each((s) => s.onUnload()));
    this.on(document, "visibilitychange", () => {
      if (document.visibilityState === "hidden") this.each((s) => s.onUnload());
    });
    // Capture phase: a handler that stops propagation must not eat the
    // event. auxclick because middle-click opens links too.
    const handle = (e: Event) => this.handleTagged(e);
    this.on(document, "click", handle, true);
    this.on(document, "auxclick", handle, true);
    this.on(document, "submit", handle, true);
  }

  private handleTagged(e: Event): void {
    if (e.type !== "submit" && (e as MouseEvent).button > 1) return; // main and middle click only
    const target = e.target as Node | null;
    let el: Element | null = target && target.nodeType === 1 ? (target as Element) : target ? target.parentElement : null;
    let name: string | null = null;
    while (el) {
      name = el.getAttribute(TAG_ATTR);
      if (name) break;
      el = el.parentElement;
    }
    if (!el || !name) return;
    // A tagged <form> fires on submit, anything else on click. Without
    // this a tagged form wrapping its own submit button counts twice.
    if ((el.tagName === "FORM") !== (e.type === "submit")) return;
    const path = typeof location !== "undefined" ? location.pathname : "";
    const tagged = name;
    this.each((s) => s.onTagged(tagged, path));
  }
}

export const runtime = new Runtime();
