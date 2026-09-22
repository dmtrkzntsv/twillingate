/* Client environment detection: OS, browser and device class.
 *
 * Pure functions over a flat list of signals. Supply a ClientSignals and
 * only its fields are consulted — a field left out is absent, never read
 * from the real browser — so a test is a table of literals and the type
 * is the complete record of what the SDK reads off the device. Omit the
 * argument and every field comes from the ambient navigator.
 *
 * The three detect* functions are views onto one resolve: OS and device
 * share their signals (iPad, maxTouchPoints, the console markers), and
 * separate passes would eventually disagree — ipados with desktop is not
 * a state that exists.
 *
 * Vocabularies mirror internal/enrich/ua.go. other means a User-Agent was
 * present and named nothing on the list; unknown means there was nothing
 * to read — a non-browser runtime. In a browser unknown is unreachable.
 */

export interface ClientSignals {
  /** navigator.userAgent */
  userAgent?: string;
  /** navigator.platform — "MacIntel" is load-bearing for iPadOS */
  platform?: string;
  /** navigator.maxTouchPoints */
  maxTouchPoints?: number;
  /** whether navigator.brave is defined — presence, not isBrave() */
  brave?: boolean;
  /** navigator.userAgentData.brands */
  brands?: { brand: string; version: string }[];
  /** navigator.userAgentData.platform */
  uaPlatform?: string;
  /** navigator.userAgentData.mobile */
  mobile?: boolean;
  /** resolved getHighEntropyValues(["platformVersion"]) */
  platformVersion?: string;
}

export interface OSInfo {
  os: string;
  osVersion: string;
  osName: string;
}
export interface BrowserInfo {
  browser: string;
  browserVersion: string;
}
export interface DeviceInfo {
  device: string;
}

interface NavigatorUA extends Navigator {
  userAgentData?: {
    brands?: { brand: string; version: string }[];
    platform?: string;
    mobile?: boolean;
    getHighEntropyValues?: (hints: string[]) => Promise<{ platformVersion?: string }>;
  };
  brave?: unknown;
}

// The one async signal. Windows 10 and 11 are indistinguishable in a
// User-Agent (both NT 10.0) and Safari freezes macOS at 10_15_7; both are
// recoverable only from userAgentData.getHighEntropyValues, which returns
// a promise. The tracker kicks it off once at init and detection reads
// whatever has resolved, so detectOS() right after init answers from the
// User-Agent and the same call a tick later carries the corrected
// version. Batches are unaffected: batchAttributes() runs in flush(),
// after the default 1000ms flushInterval has let this settle.
let resolvedPlatformVersion: string | undefined;

export function primePlatformVersion(): void {
  if (typeof navigator === "undefined") return;
  const uad = (navigator as NavigatorUA).userAgentData;
  if (!uad || typeof uad.getHighEntropyValues !== "function") return;
  uad.getHighEntropyValues(["platformVersion"]).then(
    (v) => {
      if (v && typeof v.platformVersion === "string") resolvedPlatformVersion = v.platformVersion;
    },
    () => {
      /* refused or unsupported: the synchronous parse stands */
    },
  );
}

/** Test hook: forget a resolved platformVersion between cases. */
export function resetPlatformVersion(): void {
  resolvedPlatformVersion = undefined;
}

/** Every signal, read from the ambient navigator. */
export function ambientSignals(): ClientSignals {
  if (typeof navigator === "undefined") return {};
  const n = navigator as NavigatorUA;
  const s: ClientSignals = {
    userAgent: n.userAgent,
    platform: n.platform,
    maxTouchPoints: n.maxTouchPoints,
    brave: n.brave !== undefined,
  };
  const uad = n.userAgentData;
  if (uad) {
    s.brands = uad.brands;
    s.uaPlatform = uad.platform;
    s.mobile = uad.mobile;
  }
  if (resolvedPlatformVersion !== undefined) s.platformVersion = resolvedPlatformVersion;
  return s;
}

export function detectOS(s?: ClientSignals): OSInfo {
  const r = resolve(s || ambientSignals());
  return { os: r.os, osVersion: r.osVersion, osName: r.osName };
}

export function detectBrowser(s?: ClientSignals): BrowserInfo {
  const r = resolve(s || ambientSignals());
  return { browser: r.browser, browserVersion: r.browserVersion };
}

export function detectDevice(s?: ClientSignals): DeviceInfo {
  return { device: resolve(s || ambientSignals()).device };
}

/** Everything at once, for the tracker's batch attributes. */
export function detectAll(s?: ClientSignals): OSInfo & BrowserInfo & DeviceInfo {
  return resolve(s || ambientSignals());
}

const OS_NAMES: Record<string, string> = {
  windows: "Windows", macos: "macOS", linux: "Linux", chromeos: "Chrome OS",
  ios: "iOS", ipados: "iPadOS", android: "Android", fireos: "Fire OS",
  harmonyos: "HarmonyOS", kaios: "KaiOS", tvos: "tvOS", tizen: "Tizen", webos: "webOS",
  playstation: "PlayStation", xbox: "Xbox", nintendo: "Nintendo",
};

// userAgentData.platform (Chromium >= 90) answers these without sniffing.
// It runs after the specific User-Agent checks because it reports a Fire
// tablet as Android.
const UA_PLATFORMS: Record<string, string> = {
  Windows: "windows", macOS: "macos", Android: "android", Linux: "linux",
  "Chrome OS": "chromeos", "Chromium OS": "chromeos", iOS: "ios",
};

// User-Agent markers, derivatives before bases: every Chromium UA contains
// Chrome and every Chrome UA contains Safari. The marker is also where the
// version starts, except Safari, whose version follows Version/ (Safari/
// is the WebKit build number).
const UA_BROWSERS: [marker: string, browser: string, versionMarker?: string][] = [
  ["Edg/", "edge"], ["Edge/", "edge"], ["SamsungBrowser/", "samsung_internet"],
  ["OPR/", "opera"], ["Opera/", "opera"], ["Vivaldi/", "vivaldi"], ["YaBrowser/", "yandex"],
  ["DuckDuckGo/", "duckduckgo"], ["Firefox/", "firefox"], ["FxiOS/", "firefox"],
  ["CriOS/", "chrome"], ["Chrome/", "chrome"], ["Safari/", "safari", "Version/"],
];

// userAgentData.brands, most specific first. Google Chrome and Chromium
// both mean chrome; "Not A Brand" entries match nothing.
const BRANDS: [brand: string, browser: string][] = [
  ["Microsoft Edge", "edge"], ["Opera", "opera"], ["Samsung Internet", "samsung_internet"],
  ["Vivaldi", "vivaldi"], ["Google Chrome", "chrome"], ["Chromium", "chrome"],
];

const NT: Record<string, string> = { "10.0": "10", "6.3": "8.1", "6.2": "8", "6.1": "7", "6.0": "Vista", "5.1": "XP" };

function resolve(s: ClientSignals): OSInfo & BrowserInfo & DeviceInfo {
  const ua = s.userAgent || "";
  const has = (m: string): boolean => ua.indexOf(m) >= 0;
  // iPadOS 13+ in desktop mode sends "Macintosh; Intel Mac OS X"; only
  // maxTouchPoints separates it from a Mac. The one case a User-Agent
  // cannot resolve, and the clearest thing client detection buys.
  const ipadDesktop = s.platform === "MacIntel" && (s.maxTouchPoints || 0) > 1;

  // OS: most specific first, first hit wins. Most of these User-Agents
  // are supersets of a more generic one (Fire OS contains Android, every
  // Android UA contains Linux, iPadOS in desktop mode contains Macintosh).
  let os: string;
  if (has("Xbox")) os = "xbox";
  else if (has("PlayStation")) os = "playstation";
  else if (has("Nintendo")) os = "nintendo";
  else if (has("KAIOS")) os = "kaios";
  else if (has("Tizen")) os = "tizen";
  else if (has("Web0S") || has("webOS") || has("hpwOS")) os = "webos";
  else if (has("AppleTV") || has("tvOS")) os = "tvos";
  else if (has("Silk")) os = "fireos";
  else if (has("HarmonyOS")) os = "harmonyos";
  else if (has("CrOS")) os = "chromeos";
  else if (has("iPhone") || has("iPod")) os = "ios";
  else if (has("iPad") || ipadDesktop) os = "ipados";
  else if (s.uaPlatform && UA_PLATFORMS[s.uaPlatform]) os = UA_PLATFORMS[s.uaPlatform];
  else if (has("Android")) os = "android";
  else if (has("Windows")) os = "windows";
  else if (has("Mac OS X") || has("Macintosh")) os = "macos";
  else if (has("FreeBSD") || has("OpenBSD") || has("NetBSD") || has("DragonFly")) os = "bsd";
  else if (has("X11") || has("Linux")) os = "linux";
  else if (ua || s.uaPlatform) os = "other";
  else os = "unknown";

  const osVersion = versionOf(os, ua, s.platformVersion);
  let osName = OS_NAMES[os] || "";
  if (os === "bsd") osName = (/(FreeBSD|OpenBSD|NetBSD|DragonFly)/.exec(ua) || ["", "BSD"])[1];
  if (osName && osVersion) osName += " " + osVersion;

  // Browser: brave first, because its User-Agent is deliberately Chrome's
  // and nothing later can recover it; then brands, the API built for the
  // question, which survives User-Agent reduction; then the markers.
  let browser = "";
  let browserVersion = "";
  if (s.brave) browser = "brave";
  if (!browser && s.brands) {
    for (const [brand, name] of BRANDS) {
      const b = s.brands.find((x) => x.brand === brand);
      if (b) {
        browser = name;
        browserVersion = major(b.version);
        break;
      }
    }
  }
  if (!browser) {
    for (const [marker, name, versionMarker] of UA_BROWSERS) {
      if (has(marker)) {
        browser = name;
        browserVersion = majorAfter(ua, versionMarker || marker);
        break;
      }
    }
  }
  if (!browser) browser = ua || (s.brands && s.brands.length) ? "other" : "unknown";
  if (browser === "brave" && !browserVersion) browserVersion = majorAfter(ua, "Chrome/");

  // Device: shares the OS pass. xr first, because the OS pass reports a
  // Quest as android and nothing downstream could recover it. Consoles
  // and TVs are other: $os already names each one, and they must not
  // fall through to desktop.
  let device: string;
  if (has("OculusBrowser") || has("Quest")) device = "xr";
  else if (has("Xbox") || has("PlayStation") || has("Nintendo") || has("AppleTV") || has("tvOS") ||
           has("Web0S") || has("webOS") || has("Tizen")) device = "other";
  else if (has("iPad") || has("Tablet") || ipadDesktop) device = "tablet";
  else if (s.mobile === true) device = "mobile";
  else if (has("Mobile") || has("iPhone")) device = "mobile";
  else if (ua) device = "desktop";
  else device = "unknown";

  return { os, osVersion, osName, browser, browserVersion, device };
}

function versionOf(os: string, ua: string, platformVersion?: string): string {
  const m = (re: RegExp): string => {
    const r = re.exec(ua);
    return r ? r[1].replace(/_/g, ".") : "";
  };
  switch (os) {
    case "ios":
    case "ipados":
      return m(/OS (\d+[_.]\d+(?:[_.]\d+)?)/);
    case "android":
    case "fireos":
    case "harmonyos":
      return m(/Android (\d+(?:\.\d+)*)/);
    case "windows": {
      // platformVersion major >= 13 is Windows 11, 1-12 is Windows 10;
      // Windows 7 and 8 report 0.x, so NT stands for them.
      const hi = platformVersion ? parseInt(platformVersion, 10) : 0;
      if (hi >= 13) return "11";
      if (hi >= 1) return "10";
      const nt = m(/Windows NT (\d+\.\d+)/);
      return NT[nt] || nt;
    }
    case "macos":
      return platformVersion || m(/Mac OS X (\d+[_.]\d+(?:[_.]\d+)?)/);
    case "chromeos":
      return m(/CrOS \S+ (\d+(?:\.\d+)*)/);
    default:
      return "";
  }
}

function major(version: string): string {
  return (/^\d+/.exec(version) || [""])[0];
}

// The run of digits after marker, or "" when the marker is absent or not
// followed by a digit.
function majorAfter(ua: string, marker: string): string {
  const i = ua.indexOf(marker);
  return i < 0 ? "" : major(ua.slice(i + marker.length));
}
