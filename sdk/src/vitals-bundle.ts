/* Entry of the second bundle, /js/twillingate-vitals.js: Google's
 * web-vitals package behind one global the SDK subscribes to
 * (vitals-loader.ts). Loaded only when an instance enables vitals.
 *
 * web-vitals reports final LCP, CLS and INP when the page is hidden; with
 * default options each metric reports once per page load, and again after
 * a back/forward-cache restore.
 */
import { onCLS, onFCP, onINP, onLCP, onTTFB, type Metric } from "web-vitals";
import type { VitalName, VitalsReport } from "./vitals-loader";

const subscribers: VitalsReport[] = [];
const seen: { name: VitalName; value: number }[] = [];

const fan = (m: Metric) => {
  const r = { name: m.name, value: m.value };
  seen.push(r);
  subscribers.forEach((s) => s(r));
};

// A second copy of this file (two SDK copies on one page, each loading
// it) leaves the first in place: its observers already cover the page.
if (!window.twillingateVitals) {
  onLCP(fan);
  onINP(fan);
  onCLS(fan);
  onFCP(fan);
  onTTFB(fan);

  window.twillingateVitals = (cb) => {
    subscribers.push(cb);
    seen.forEach((m) => cb(m)); // a late subscriber still gets early FCP/TTFB
  };
  window.dispatchEvent(new Event("twillingate-vitals"));
}
