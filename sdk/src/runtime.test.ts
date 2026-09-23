// The runtime installs every browser hook once and fans out to every
// subscriber; each subscriber decides for itself.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { runtime, type NavigationSource, type Subscriber } from "./runtime";

function sub(): Subscriber & { nav: NavigationSource[]; online: number; unload: number; tagged: Array<[string, string]> } {
  const s = {
    nav: [] as NavigationSource[], online: 0, unload: 0, tagged: [] as Array<[string, string]>,
    onNavigate(source: NavigationSource) { s.nav.push(source); },
    onOnline() { s.online++; },
    onUnload() { s.unload++; },
    onTagged(name: string, path: string) { s.tagged.push([name, path]); },
  };
  return s;
}

const nativePushState = history.pushState;

beforeEach(() => {
  runtime.reset();
  history.replaceState(null, "", "/start");
  document.body.innerHTML = "";
});

afterEach(() => {
  runtime.reset();
  vi.restoreAllMocks();
  expect(history.pushState).toBe(nativePushState);
});

describe("runtime", () => {
  it("installs nothing until the first subscriber, then once", () => {
    expect(runtime.installed()).toBe(false);
    const a = sub();
    const b = sub();
    runtime.subscribe(a);
    expect(runtime.installed()).toBe(true);
    const patched = history.pushState;
    runtime.subscribe(b);
    runtime.subscribe(a); // idempotent
    expect(history.pushState).toBe(patched);
    expect(runtime.subscribers()).toBe(2);
  });

  it("one pushState reaches every subscriber as one navigation each", () => {
    const a = sub();
    const b = sub();
    runtime.subscribe(a);
    runtime.subscribe(b);
    history.pushState(null, "", "/second");
    expect(location.pathname).toBe("/second"); // the native call still ran
    expect(a.nav).toEqual(["push"]);
    expect(b.nav).toEqual(["push"]);
    window.dispatchEvent(new Event("popstate"));
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    expect(a.nav).toEqual(["push", "pop", "hash"]);
  });

  it("dispatches online and unload", () => {
    const a = sub();
    runtime.subscribe(a);
    window.dispatchEvent(new Event("online"));
    window.dispatchEvent(new Event("pagehide"));
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(a.online).toBe(1);
    expect(a.unload).toBe(2);
  });

  it("a subscriber that throws does not stop the others", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const bad = { ...sub(), onNavigate() { throw new Error("boom"); } };
    const good = sub();
    runtime.subscribe(bad);
    runtime.subscribe(good);
    history.pushState(null, "", "/x");
    expect(good.nav).toEqual(["push"]);
  });

  it("tagged elements: click on the element or a descendant, main and middle button, nearest ancestor wins", () => {
    document.body.innerHTML =
      '<div data-twillingate-event="outer"><button id="b" data-twillingate-event="signup"><span id="s">Go</span></button></div>' +
      '<a id="plain">no tag</a>';
    const a = sub();
    runtime.subscribe(a);
    document.getElementById("s")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    document.getElementById("b")!.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, button: 1 }));
    document.getElementById("b")!.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, button: 2 }));
    document.getElementById("plain")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    expect(a.tagged).toEqual([["signup", "/start"], ["signup", "/start"]]);
  });

  it("tagged forms fire once per submit, not on the click that submits them", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    document.body.innerHTML =
      '<form id="f" data-twillingate-event="subscribe"><button id="fb" type="submit">Go</button></form>';
    const a = sub();
    runtime.subscribe(a);
    // Clicking the submit button is a click on a descendant of a tagged
    // <form>: the click itself is ignored, and the submit the browser
    // dispatches for it counts once.
    document.getElementById("fb")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    expect(a.tagged).toEqual([["subscribe", "/start"]]);
    document.getElementById("f")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(a.tagged).toEqual([["subscribe", "/start"], ["subscribe", "/start"]]);
  });

  it("a handler that stops propagation cannot eat a tagged click", () => {
    document.body.innerHTML = '<button id="b" data-twillingate-event="cta">Go</button>';
    document.getElementById("b")!.addEventListener("click", (e) => e.stopPropagation());
    const a = sub();
    runtime.subscribe(a);
    document.getElementById("b")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    expect(a.tagged).toEqual([["cta", "/start"]]);
  });

  it("reset() removes every hook and restores pushState", () => {
    const a = sub();
    runtime.subscribe(a);
    runtime.reset();
    expect(runtime.installed()).toBe(false);
    history.pushState(null, "", "/after");
    window.dispatchEvent(new Event("online"));
    expect(a.nav).toEqual([]);
    expect(a.online).toBe(0);
  });
});
