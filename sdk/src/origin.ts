/* The collector's origin, baked into the served file.
 *
 * The collector replaces this placeholder, per request, with the origin
 * the file was requested from (internal/server/script.go), the same way
 * it replaces __TWILLINGATE_VERSION__. A build that still carries the
 * placeholder was not served by a collector -- someone bundled the
 * module -- and has no idea where to post; init() then warns and stays
 * dormant. Tests mock this module to supply an origin.
 */
export const ORIGIN = "__TWILLINGATE_URL__";

/** The origin batches are posted to, or null when this build has none. */
export function collectorOrigin(): string | null {
  if (ORIGIN.slice(0, 2) === "__") return null;
  return ORIGIN.replace(/\/$/, "");
}
