// Tagged forms (data-twillingate-form) and submitForm: one POST to
// /ingest/forms/{name}, retried in memory, never written to storage.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { runtime } from "./runtime";
import type { StorageDriver } from "./storage";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

interface Call {
  url: string;
  init: { method?: string; body: string; keepalive?: boolean; headers?: unknown };
  body: { key: string; id: string; fields: Record<string, unknown>; attributes: Record<string, unknown> };
}
let calls: Call[];
let statuses: Array<number | "network">;

function tg(opts: Partial<InitOptions> = {}, name?: string): Twillingate {
  const t = new Twillingate(name);
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}

function formEl(html: string): HTMLFormElement {
  document.body.innerHTML = html;
  return document.querySelector("form") as HTMLFormElement;
}

const CONTACT =
  '<form data-twillingate-form="contact">' +
  '<input name="email" value="a@b.c"><textarea name="message">hello</textarea>' +
  '<input type="hidden" name="$redirect" value=""><input type="hidden" name="$id" value="x">' +
  '<input type="file" name="cv"></form>';

function submit(form: HTMLFormElement): SubmitEvent {
  const ev = new Event("submit", { bubbles: true, cancelable: true }) as SubmitEvent;
  form.dispatchEvent(ev);
  return ev;
}

async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}

beforeEach(() => {
  runtime.reset();
  vi.useFakeTimers();
  calls = [];
  statuses = [];
  vi.stubGlobal("fetch", (url: string, init: Call["init"]) => {
    calls.push({ url, init, body: JSON.parse(init.body) });
    const s = statuses.shift() ?? 201;
    if (s === "network") return Promise.reject(new TypeError("Failed to fetch"));
    return Promise.resolve({ status: s, ok: s >= 200 && s < 300 });
  });
  localStorage.clear();
  history.replaceState(null, "", "/pricing");
  document.body.innerHTML = "";
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("a tagged form", () => {
  it("posts once to /ingest/forms/{name} with its fields and the page, minus $ fields and files", async () => {
    tg();
    const form = formEl(CONTACT);
    const ev = submit(form);
    await settle();
    expect(ev.defaultPrevented).toBe(true);
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("https://collector.example.com/ingest/forms/contact");
    expect(calls[0].init.method).toBe("POST");
    expect(calls[0].init.keepalive).toBe(true);
    expect(calls[0].init.headers).toBeUndefined(); // no Content-Type: text/plain, CORS-simple
    const b = calls[0].body;
    expect(b.key).toBe("ak_test");
    expect(b.fields).toEqual({ email: "a@b.c", message: "hello" });
    expect(b.id).toMatch(/^[0-9a-f-]{36}$/);
    expect(b.attributes).toEqual({ $host: "example.com", $path: "/pricing" });
  });

  it("joins repeated names with a comma and a space", async () => {
    tg();
    const form = formEl(
      '<form data-twillingate-form="poll"><input type="checkbox" name="c" value="a" checked>' +
        '<input type="checkbox" name="c" value="b" checked><input type="checkbox" name="c" value="z"></form>',
    );
    submit(form);
    await settle();
    expect(calls[0].body.fields).toEqual({ c: "a, b" });
  });

  it("ignores a second submit while the first is in flight", async () => {
    tg();
    const form = formEl(CONTACT);
    const first = submit(form);
    expect(form.getAttribute("aria-busy")).toBe("true");
    const second = submit(form);
    await settle();
    expect(first.defaultPrevented).toBe(true);
    expect(second.defaultPrevented).toBe(true);
    expect(calls).toHaveLength(1);
    expect(form.hasAttribute("aria-busy")).toBe(false);
  });

  it("on success resets the form and sets the success fragment", async () => {
    tg();
    const form = formEl(CONTACT);
    (form.elements.namedItem("email") as HTMLInputElement).value = "typed@x.y";
    submit(form);
    await settle();
    expect((form.elements.namedItem("email") as HTMLInputElement).value).toBe("a@b.c"); // reset to the default
    expect(location.hash).toBe("#twillingate-form-success-contact");
  });

  it("with $redirect navigates there with the fragment, replacing an existing one", async () => {
    tg();
    const form = formEl(CONTACT);
    (form.elements.namedItem("$redirect") as HTMLInputElement).value = "https://example.com/pricing#old";
    const typed = form.elements.namedItem("email") as HTMLInputElement;
    typed.value = "typed@x.y";
    submit(form);
    await settle();
    expect(location.href).toBe("https://example.com/pricing#twillingate-form-success-contact");
    expect(calls[0].body.fields).not.toHaveProperty("$redirect");
    expect(typed.value).toBe("typed@x.y"); // leaving the page: no reset
  });

  it("refuses a $redirect that is not http(s) and stays on the page", async () => {
    tg();
    const form = formEl(CONTACT);
    (form.elements.namedItem("$redirect") as HTMLInputElement).value = "javascript:alert(1)";
    submit(form);
    await settle();
    expect(location.href).toBe("https://example.com/pricing#twillingate-form-success-contact");
  });

  it("retries a 5xx and a network error at 1 s, 5 s and 25 s with the same id", async () => {
    tg();
    statuses = [500, "network", 502, 201];
    const form = formEl(CONTACT);
    submit(form);
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(999);
    expect(calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(calls).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(4999);
    expect(calls).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(calls).toHaveLength(3);
    await vi.advanceTimersByTimeAsync(24999);
    expect(calls).toHaveLength(3);
    await vi.advanceTimersByTimeAsync(1);
    expect(calls).toHaveLength(4);
    expect(new Set(calls.map((c) => c.body.id)).size).toBe(1);
    expect(location.hash).toBe("#twillingate-form-success-contact");
  });

  it("gives up after the third retry: the error fragment", async () => {
    tg();
    statuses = [500, 500, 500, 500];
    const form = formEl(CONTACT);
    const events: CustomEvent[] = [];
    form.addEventListener("twillingate:form", (e) => events.push(e as CustomEvent));
    submit(form);
    await vi.advanceTimersByTimeAsync(31000);
    expect(calls).toHaveLength(4);
    expect(location.hash).toBe("#twillingate-form-error-contact");
    expect(events[0].detail.status).toBe("error");
  });

  it("a 4xx is an error at once: no retry, the error fragment, the form kept as typed", async () => {
    tg();
    statuses = [400];
    const form = formEl(CONTACT);
    const email = form.elements.namedItem("email") as HTMLInputElement;
    email.value = "typed@x.y";
    submit(form);
    await vi.advanceTimersByTimeAsync(60000);
    expect(calls).toHaveLength(1);
    expect(location.hash).toBe("#twillingate-form-error-contact");
    expect(email.value).toBe("typed@x.y");
    expect(form.hasAttribute("aria-busy")).toBe(false);
  });

  it("an error with $redirect navigates with the error fragment", async () => {
    tg();
    statuses = [409];
    const form = formEl(CONTACT);
    (form.elements.namedItem("$redirect") as HTMLInputElement).value = "https://example.com/pricing";
    submit(form);
    await settle();
    expect(location.href).toBe("https://example.com/pricing#twillingate-form-error-contact");
  });

  it("dispatches twillingate:form on the form, bubbling, with name, status and id", async () => {
    tg();
    const form = formEl(CONTACT);
    const seen: CustomEvent[] = [];
    document.addEventListener("twillingate:form", (e) => seen.push(e as CustomEvent));
    submit(form);
    await settle();
    expect(seen).toHaveLength(1);
    expect(seen[0].target).toBe(form);
    expect(seen[0].detail).toEqual({ name: "contact", status: "success", id: calls[0].body.id });
  });

  it("an invalid name is an error without a request", async () => {
    tg();
    const form = formEl('<form data-twillingate-form="Not Valid!"><input name="a" value="1"></form>');
    const seen: CustomEvent[] = [];
    form.addEventListener("twillingate:form", (e) => seen.push(e as CustomEvent));
    const ev = submit(form);
    await settle();
    expect(ev.defaultPrevented).toBe(true);
    expect(calls).toHaveLength(0);
    expect(seen[0].detail.status).toBe("error");
    expect(form.hasAttribute("aria-busy")).toBe(false);
  });

  it("fires no data-twillingate-event, and sends no events", async () => {
    const t = tg();
    const form = formEl(CONTACT);
    form.setAttribute("data-twillingate-event", "contact_form");
    submit(form);
    await settle();
    t.flush();
    await settle();
    expect(calls.map((c) => c.url)).toEqual(["https://collector.example.com/ingest/forms/contact"]);
  });

  it("sends once with several instances: the first registered one", async () => {
    tg({ key: "ak_first" });
    tg({ key: "ak_second" }, "second");
    const form = formEl(CONTACT);
    submit(form);
    await settle();
    expect(calls).toHaveLength(1);
    expect(calls[0].body.key).toBe("ak_first");
  });

  it("an untagged form is left alone", async () => {
    tg();
    const form = formEl('<form><input name="a" value="1"></form>');
    const ev = submit(form);
    await settle();
    expect(ev.defaultPrevented).toBe(false);
    expect(calls).toHaveLength(0);
  });
});

describe("identity on a form", () => {
  it("an identified instance sends $user_id and $install_id, and only those two", async () => {
    const t = new Twillingate();
    t.identify("u_1", "Ada");
    t.installId("inst_1");
    t.group("g_1");
    t.init({ key: "ak_test", identity: "identified", flushInterval: 0, autoPageviews: false });
    submit(formEl(CONTACT));
    await settle();
    expect(calls[0].body.attributes).toEqual({
      $host: "example.com", $path: "/pricing", $user_id: "u_1", $install_id: "inst_1",
    });
  });

  it("an anonymous instance sends neither", async () => {
    tg();
    submit(formEl(CONTACT));
    await settle();
    expect(Object.keys(calls[0].body.attributes).sort()).toEqual(["$host", "$path"]);
  });

  it("an opted-out visitor's submission is still sent, without identity keys", async () => {
    const t = new Twillingate();
    t.identify("u_1");
    t.installId("inst_1");
    t.init({ key: "ak_test", identity: "identified", flushInterval: 0, autoPageviews: false, optOut: true });
    submit(formEl(CONTACT));
    await settle();
    expect(calls).toHaveLength(1);
    expect(calls[0].body.attributes).toEqual({ $host: "example.com", $path: "/pricing" });
  });

  it("the twillingate_ignore flag opts out the same way", async () => {
    const t = new Twillingate();
    t.installId("inst_1");
    t.init({ key: "ak_test", identity: "identified", flushInterval: 0, autoPageviews: false });
    t.optOut(true);
    submit(formEl(CONTACT));
    await settle();
    expect(calls[0].body.attributes).toEqual({ $host: "example.com", $path: "/pricing" });
  });
});

describe("storage", () => {
  function spyDriver() {
    const writes: string[][] = [];
    const store = new Map<string, string>();
    const driver: StorageDriver = {
      get: (k) => store.get(k) ?? null,
      set: (k, v) => {
        writes.push(["set", k, v]);
        store.set(k, v);
      },
      remove: (k) => {
        writes.push(["remove", k]);
        store.delete(k);
      },
    };
    return { driver, writes };
  }

  it("a submission, delivered or failed, never reaches the storage driver", async () => {
    const { driver, writes } = spyDriver();
    const t = new Twillingate();
    t.installId("inst_1");
    t.init({ key: "ak_test", identity: "identified", consent: true, storage: driver, flushInterval: 0, autoPageviews: false });
    writes.length = 0;
    localStorage.clear();
    statuses = [500, 500, 500, 500, 201];
    submit(formEl(CONTACT));
    await vi.advanceTimersByTimeAsync(40000);
    submit(formEl(CONTACT));
    await vi.advanceTimersByTimeAsync(40000);
    expect(calls.length).toBeGreaterThan(1);
    expect(writes).toEqual([]);
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain("a@b.c");
  });
});

describe("submitForm", () => {
  it("takes a flat object and resolves to {id}, touching neither hash nor page", async () => {
    const t = tg();
    const p = t.submitForm("contact", { email: "a@b.c", seats: 5, trial: true });
    await settle();
    const r = await p;
    expect(r).toEqual({ id: calls[0].body.id });
    expect(calls[0].url).toBe("https://collector.example.com/ingest/forms/contact");
    expect(calls[0].body.fields).toEqual({ email: "a@b.c", seats: 5, trial: true });
    expect(location.hash).toBe("");
  });

  it("takes a FormData: files and $ names skipped, repeated names joined", async () => {
    const t = tg();
    const fd = new FormData();
    fd.append("a", "1");
    fd.append("a", "2");
    fd.append("$redirect", "https://example.com/");
    fd.append("cv", new File(["x"], "cv.txt"));
    const p = t.submitForm("contact", fd);
    await settle();
    await p;
    expect(calls[0].body.fields).toEqual({ a: "1, 2" });
  });

  it("takes an HTMLFormElement, tagged or not", async () => {
    const t = tg();
    const form = formEl('<form><input name="email" value="a@b.c"></form>');
    const p = t.submitForm("contact", form);
    await settle();
    await p;
    expect(calls[0].body.fields).toEqual({ email: "a@b.c" });
    expect(location.hash).toBe("");
  });

  it("rejects a 4xx at once", async () => {
    const t = tg();
    statuses = [403];
    const p = t.submitForm("contact", { a: "1" });
    const assertion = expect(p).rejects.toThrow(/403/);
    await vi.advanceTimersByTimeAsync(60000);
    await assertion;
    expect(calls).toHaveLength(1);
  });

  it("retries a 5xx with the same id and resolves", async () => {
    const t = tg();
    statuses = [503, 201];
    const p = t.submitForm("contact", { a: "1" });
    await vi.advanceTimersByTimeAsync(1000);
    await expect(p).resolves.toEqual({ id: calls[0].body.id });
    expect(calls).toHaveLength(2);
    expect(calls[1].body.id).toBe(calls[0].body.id);
  });

  it("rejects once the retries run out", async () => {
    const t = tg();
    statuses = ["network", "network", "network", "network"];
    const p = t.submitForm("contact", { a: "1" });
    const assertion = expect(p).rejects.toThrow();
    await vi.advanceTimersByTimeAsync(31000);
    await assertion;
    expect(calls).toHaveLength(4);
  });

  it("sends keepalive for a small body and none above 60000 bytes", async () => {
    const t = tg();
    const small = t.submitForm("contact", { a: "x".repeat(1000) });
    await settle();
    await small;
    expect(calls[0].init.keepalive).toBe(true);
    // fetch throws a TypeError for a keepalive body over 64 KB; this one
    // is 70 000 bytes of multi-byte text, counted in bytes not characters.
    const big = t.submitForm("contact", { a: "é".repeat(35000) });
    await settle();
    await big;
    expect(calls).toHaveLength(2);
    expect(calls[1].init.keepalive).toBeUndefined();
    expect(calls[1].init.method).toBe("POST");
  });

  it("skips $ keys and non-primitive values of a plain object", async () => {
    const t = tg();
    const p = t.submitForm("contact", { a: "1", $redirect: "https://x.y/", n: null, o: { x: 1 }, l: [1] } as never);
    await settle();
    await p;
    expect(calls[0].body.fields).toEqual({ a: "1" });
  });

  it("rejects an invalid name without a request", async () => {
    const t = tg();
    await expect(t.submitForm("Bad Name", { a: "1" })).rejects.toThrow(/name/);
    await expect(t.submitForm("", { a: "1" })).rejects.toThrow(/name/);
    expect(calls).toHaveLength(0);
  });

  it("waits for init() like the other calls", async () => {
    const t = new Twillingate();
    const p = t.submitForm("contact", { a: "1" });
    await settle();
    expect(calls).toHaveLength(0);
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false });
    await settle();
    await expect(p).resolves.toEqual({ id: calls[0].body.id });
  });
});
