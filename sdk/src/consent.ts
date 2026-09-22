/**
 * What data-consent and init({consent}) accept. The attribute can only
 * carry a string; init additionally takes the native values. A string
 * resolves through this one function either way, so the two entry points
 * cannot drift apart (mask.ts is the model).
 */
export type ConsentSpec = boolean | string | (() => unknown);

/**
 * Resolve a consent spec to a function the SDK calls at every storage
 * decision. Literals become constants. A name is looked up on globalThis
 * at each call, never at resolve time, so a variable a consent manager
 * flips after page load is picked up live; a function is called each time
 * and its result coerced to a boolean.
 *
 * Anything that cannot answer — a name that resolves to nothing, a function
 * that throws — fails closed to false with one console warning. Masking
 * fails closed by dropping pageviews; consent fails closed by writing
 * nothing.
 */
export function resolveConsent(spec: ConsentSpec | undefined | null): () => boolean {
  if (spec === undefined || spec === null || spec === false || spec === "" || spec === "false") {
    return () => false;
  }
  if (spec === true || spec === "true") return () => true;

  let warned = false;
  const fail = (what: string): false => {
    if (!warned) {
      warned = true;
      console.warn(`twillingate: ${what}, treating as no consent`);
    }
    return false;
  };
  const call = (fn: () => unknown, what: string): boolean => {
    try {
      return Boolean(fn());
    } catch (e) {
      return fail(`${what} threw (${String(e)})`);
    }
  };

  if (typeof spec === "function") return () => call(spec, "the consent function");

  const name = spec;
  return () => {
    const v = (globalThis as Record<string, unknown>)[name];
    if (v === undefined) return fail(`consent names nothing on window: ${name}`);
    if (typeof v === "function") return call(v as () => unknown, `consent function ${name}`);
    return Boolean(v);
  };
}
