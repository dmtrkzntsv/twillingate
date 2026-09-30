/* Loads the Web Vitals bundle (vitals-bundle.ts, served at
 * /js/twillingate-vitals.js) on first use and subscribes to it. Lives in
 * the main bundle; web-vitals itself never does, so a site without vitals
 * pays nothing for them.
 */
import { collectorOrigin } from "./origin";

export type VitalName = "LCP" | "INP" | "CLS" | "FCP" | "TTFB";

/**
 * One value the vitals bundle reports. id is web-vitals' metric id: a
 * re-report of the same metric keeps it, a back/forward-cache restore
 * starts a new one.
 */
export type VitalsReport = (m: { name: VitalName; value: number; id: string }) => void;

declare global {
  interface Window {
    /** Defined by twillingate-vitals.js: subscribe to every vital, earlier ones replayed. */
    twillingateVitals?: (cb: VitalsReport) => void;
  }
}

// The event the vitals bundle dispatches once it has defined the global.
const READY = "twillingate-vitals";

let loading = false;

/**
 * Hand cb to the vitals bundle, loading it once per page whatever the
 * number of instances asking.
 */
export function requestVitals(cb: VitalsReport): void {
  if (window.twillingateVitals) return window.twillingateVitals(cb);
  window.addEventListener(READY, () => window.twillingateVitals?.(cb), { once: true });
  if (loading) return;
  const origin = collectorOrigin();
  if (!origin) return;
  loading = true;
  // No integrity attribute: the script comes from the collector's own
  // origin, the one that served this SDK and so already trusted to the
  // same degree, and its content changes with each release (the version
  // is substituted at serve time), so there is no fixed hash to pin.
  const s = document.createElement("script");
  s.src = origin + "/js/twillingate-vitals.js";
  s.async = true;
  document.head.appendChild(s);
}

/** Test hook: forget that the bundle was requested. */
export function resetVitalsLoader(): void {
  loading = false;
}
