/* Where an instance keeps its keys once consent allows: the built-in
 * drivers, a custom object, and the two person-level flags that always
 * live in localStorage regardless of driver, because opting out and
 * debugging are about the page, not one instance.
 */

export interface StorageDriver {
  get(key: string): string | null;
  set(key: string, value: string): void;
  remove(key: string): void;
}

export type StorageSpec = "localStorage" | "sessionStorage" | "memory" | "cookie" | StorageDriver;

export const IGNORE_FLAG = "twillingate_ignore";
export const DEBUG_FLAG = "twillingate_debug";

// One year, the same lifetime every analytics cookie uses.
const COOKIE_MAX_AGE = 31536000;

/** Wrap any driver so a throw counts as "storage unavailable". */
function guarded(d: StorageDriver): StorageDriver {
  return {
    get(key) {
      try {
        return d.get(key);
      } catch {
        return null;
      }
    },
    set(key, value) {
      try {
        d.set(key, value);
      } catch {
        /* unavailable: nothing persists */
      }
    },
    remove(key) {
      try {
        d.remove(key);
      } catch {
        /* unavailable */
      }
    },
  };
}

function webStorage(which: "localStorage" | "sessionStorage"): StorageDriver {
  return guarded({
    get: (key) => window[which].getItem(key),
    set: (key, value) => window[which].setItem(key, value),
    remove: (key) => window[which].removeItem(key),
  });
}

/** An in-instance map: equivalent to no consent, useful for tests and apps that hold identity themselves. */
export function memoryDriver(): StorageDriver {
  const m = new Map<string, string>();
  return {
    get: (key) => (m.has(key) ? (m.get(key) as string) : null),
    set: (key, value) => void m.set(key, value),
    remove: (key) => void m.delete(key),
  };
}

/**
 * Host-only cookies, one per identity key. The retry queue is skipped: a
 * queue in a cookie would ride on every request to the site. A cookie on
 * a shared parent domain is a custom driver (docs/twillingate.md shows
 * one), since the SDK cannot know the registrable domain.
 */
export function cookieDriver(): StorageDriver {
  const isQueue = (key: string) => key.slice(-6) === "_queue";
  const secure = typeof location !== "undefined" && location.protocol === "https:" ? "; Secure" : "";
  return guarded({
    get(key) {
      if (isQueue(key)) return null;
      const m = new RegExp("(?:^|; )" + key.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "=([^;]*)").exec(document.cookie);
      return m ? decodeURIComponent(m[1]) : null;
    },
    set(key, value) {
      if (isQueue(key)) return;
      document.cookie = `${key}=${encodeURIComponent(value)}; path=/; max-age=${COOKIE_MAX_AGE}; SameSite=Lax${secure}`;
    },
    remove(key) {
      if (isQueue(key)) return;
      document.cookie = `${key}=; path=/; max-age=0; SameSite=Lax${secure}`;
    },
  });
}

/** Resolve the storage option to a driver. Unusable input warns and keeps localStorage. */
export function resolveStorage(spec: StorageSpec | undefined): StorageDriver {
  if (spec === undefined || spec === "localStorage") return webStorage("localStorage");
  if (spec === "sessionStorage") return webStorage("sessionStorage");
  if (spec === "memory") return memoryDriver();
  if (spec === "cookie") return cookieDriver();
  if (
    spec && typeof spec === "object" &&
    typeof spec.get === "function" && typeof spec.set === "function" && typeof spec.remove === "function"
  ) {
    return guarded(spec);
  }
  console.warn(`twillingate: storage ${JSON.stringify(spec)} is not a driver; using localStorage`);
  return webStorage("localStorage");
}

/** The person-level flags: always localStorage, never prefixed, read live. */
export function readFlag(name: string): boolean {
  try {
    return localStorage.getItem(name) === "true";
  } catch {
    return false;
  }
}

export function writeFlag(name: string, on: boolean): void {
  try {
    if (on) localStorage.setItem(name, "true");
    else localStorage.removeItem(name);
  } catch {
    /* storage unavailable */
  }
}
