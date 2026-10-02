/* Entry of the second bundle, /js/twillingate-vitals.js: Google's
 * web-vitals package behind one global the SDK subscribes to
 * (vitals-loader.ts). Loaded only when an instance enables vitals.
 *
 * web-vitals reports LCP, CLS and INP when the page is hidden, and reports
 * CLS and INP again, under the same metric id, on every later hide that
 * finds them grown (a tab switch and return). The SDK sends the first
 * report per id and ignores the rest. A back/forward-cache restore starts
 * new ids, so a restored page reports again.
 */
import { onCLS, onFCP, onINP, onLCP, onTTFB, type Metric } from "web-vitals";
import type { VitalName, VitalsReport } from "./vitals-loader";

const subscribers: VitalsReport[] = [];
const seen: { name: VitalName; value: number; id: string }[] = [];

const fan = (m: Metric) => {
  const r = { name: m.name, value: m.value, id: m.id };
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
