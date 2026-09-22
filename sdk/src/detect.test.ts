// Detection is a table: every case is a ClientSignals literal, so no case
// depends on what jsdom happens to provide, and the type is the whole
// record of what the SDK reads off the device.
import { describe, expect, it } from "vitest";
import { detectBrowser, detectDevice, detectOS, type ClientSignals } from "./detect";

const UA = {
  chromeWin: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
  safariMac: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
  safariIphone: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
  safariIpad: "Mozilla/5.0 (iPad; CPU OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
  chromeIos: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/126.0.6478.54 Mobile/15E148 Safari/604.1",
  chromeAndroid: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
  androidTablet: "Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
  samsung: "Mozilla/5.0 (Linux; Android 13; SAMSUNG SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36",
  silk: "Mozilla/5.0 (Linux; Android 9; KFMAWI) AppleWebKit/537.36 (KHTML, like Gecko) Silk/120.5.1 like Chrome/120.0.6099.230 Safari/537.36",
  harmony: "Mozilla/5.0 (Linux; Android 10; HarmonyOS; NOH-AN00) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/92.0.4515.105 HuaweiBrowser/12.0.3.310 Mobile Safari/537.36",
  firefoxLinux: "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
  freebsd: "Mozilla/5.0 (X11; FreeBSD amd64; rv:127.0) Gecko/20100101 Firefox/127.0",
  cros: "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
  edge: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.2592.87",
  opera: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 OPR/111.0.0.0",
  vivaldi: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Vivaldi/6.8.3381.46",
  yandex: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 YaBrowser/24.6.0.0 Safari/537.36",
  duckduckgo: "Mozilla/5.0 (Linux; Android 10; SM-G960F) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.0.0 Mobile DuckDuckGo/5 Safari/537.36",
  kaios: "Mozilla/5.0 (Mobile; Nokia 8110 4G; rv:48.0) Gecko/48.0 Firefox/48.0 KAIOS/2.5",
  quest: "Mozilla/5.0 (Linux; Android 12; Quest 3) AppleWebKit/537.36 (KHTML, like Gecko) OculusBrowser/33.0 Chrome/126.0.0.0 VR Safari/537.36",
  playstation: "Mozilla/5.0 (PlayStation; PlayStation 5/8.20) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
  xbox: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; Xbox; Xbox Series X) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edge/44.18363.8131",
  nintendo: "Mozilla/5.0 (Nintendo Switch; WebApplet) AppleWebKit/609.4 (KHTML, like Gecko) NF/6.0.2.22.4 NintendoBrowser/5.1.0.23519",
  tizen: "Mozilla/5.0 (SMART-TV; LINUX; Tizen 6.0) AppleWebKit/537.36 (KHTML, like Gecko) 76.0.3809.146/6.0 TV Safari/537.36",
  webos: "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.79 Safari/537.36 WebAppManager",
  appletv: "AppleTV11,1/11.1",
  unknownThing: "SomeNewRuntime/1.0",
};

describe("detectOS", () => {
  // One row per vocabulary value detection can reach, with a real
  // User-Agent, plus the version each one yields.
  const table: [string, ClientSignals, string, string][] = [
    ["windows", { userAgent: UA.chromeWin }, "windows", "10"],
    ["macos", { userAgent: UA.safariMac }, "macos", "10.15.7"],
    ["linux", { userAgent: UA.firefoxLinux }, "linux", ""],
    ["bsd", { userAgent: UA.freebsd }, "bsd", ""],
    ["chromeos", { userAgent: UA.cros }, "chromeos", "14541.0.0"],
    ["ios", { userAgent: UA.safariIphone }, "ios", "17.2"],
    ["ipados", { userAgent: UA.safariIpad }, "ipados", "17.2"],
    ["android", { userAgent: UA.chromeAndroid }, "android", "14"],
    ["fireos", { userAgent: UA.silk }, "fireos", "9"],
    ["harmonyos", { userAgent: UA.harmony }, "harmonyos", "10"],
    ["kaios", { userAgent: UA.kaios }, "kaios", ""],
    ["tvos", { userAgent: UA.appletv }, "tvos", ""],
    ["tizen", { userAgent: UA.tizen }, "tizen", ""],
    ["webos", { userAgent: UA.webos }, "webos", ""],
    ["playstation", { userAgent: UA.playstation }, "playstation", ""],
    ["xbox", { userAgent: UA.xbox }, "xbox", ""],
    ["nintendo", { userAgent: UA.nintendo }, "nintendo", ""],
  ];
  it.each(table)("%s", (_name, signals, os, osVersion) => {
    expect(detectOS(signals)).toMatchObject({ os, osVersion });
  });

  it("orders specific before generic", () => {
    expect(detectOS({ userAgent: UA.silk }).os).toBe("fireos"); // contains Android
    expect(detectOS({ userAgent: UA.chromeAndroid }).os).toBe("android"); // contains Linux
    expect(detectOS({ userAgent: UA.freebsd }).os).toBe("bsd"); // contains X11
    expect(detectOS({ userAgent: UA.xbox }).os).toBe("xbox"); // contains Windows NT
  });

  it("separates iPadOS in desktop mode from a Mac by maxTouchPoints", () => {
    const r = detectOS({ userAgent: UA.safariMac, platform: "MacIntel", maxTouchPoints: 5 });
    expect(r.os).toBe("ipados");
    expect(detectOS({ userAgent: UA.safariMac, platform: "MacIntel", maxTouchPoints: 0 }).os).toBe("macos");
  });

  it("uses userAgentData.platform after the specific checks, not before", () => {
    expect(detectOS({ userAgent: UA.silk, uaPlatform: "Android" }).os).toBe("fireos");
    expect(detectOS({ uaPlatform: "Chrome OS" }).os).toBe("chromeos");
    expect(detectOS({ uaPlatform: "Windows" }).os).toBe("windows");
  });

  it("resolves Windows 11 and a true macOS version from platformVersion", () => {
    expect(detectOS({ userAgent: UA.chromeWin, platformVersion: "15.0.0" })).toMatchObject({ osVersion: "11", osName: "Windows 11" });
    expect(detectOS({ userAgent: UA.chromeWin, platformVersion: "10.0.0" })).toMatchObject({ osVersion: "10", osName: "Windows 10" });
    expect(detectOS({ userAgent: UA.chromeWin, platformVersion: "0.0.0" }).osVersion).toBe("10"); // Windows 7/8 report 0.x: NT stands
    expect(detectOS({ userAgent: UA.safariMac, platformVersion: "14.2.1" })).toMatchObject({ osVersion: "14.2.1", osName: "macOS 14.2.1" });
  });

  it("falls back to the synchronous parse without platformVersion", () => {
    expect(detectOS({ userAgent: UA.chromeWin })).toMatchObject({ osVersion: "10", osName: "Windows 10" });
    expect(detectOS({ userAgent: UA.safariMac }).osName).toBe("macOS 10.15.7");
    expect(detectOS({ userAgent: UA.freebsd }).osName).toBe("FreeBSD");
  });

  it("composes osName from the display name and version", () => {
    expect(detectOS({ userAgent: UA.safariIphone }).osName).toBe("iOS 17.2");
    expect(detectOS({ userAgent: UA.firefoxLinux }).osName).toBe("Linux");
  });

  it("is other for an unrecognised User-Agent and unknown for none", () => {
    expect(detectOS({ userAgent: UA.unknownThing })).toEqual({ os: "other", osVersion: "", osName: "" });
    expect(detectOS({})).toEqual({ os: "unknown", osVersion: "", osName: "" });
    expect(detectOS({ userAgent: "" })).toEqual({ os: "unknown", osVersion: "", osName: "" });
  });

  it("never returns the declare-only values", () => {
    for (const ua of Object.values(UA)) {
      expect(["watchos", "visionos"]).not.toContain(detectOS({ userAgent: ua }).os);
    }
  });
});

describe("detectBrowser", () => {
  const table: [string, ClientSignals, string, string][] = [
    ["chrome", { userAgent: UA.chromeWin }, "chrome", "126"],
    ["chrome on iOS", { userAgent: UA.chromeIos }, "chrome", "126"],
    ["safari", { userAgent: UA.safariMac }, "safari", "17"],
    ["firefox", { userAgent: UA.firefoxLinux }, "firefox", "127"],
    ["edge", { userAgent: UA.edge }, "edge", "126"],
    ["legacy edge", { userAgent: UA.xbox }, "edge", "44"],
    ["opera", { userAgent: UA.opera }, "opera", "111"],
    ["samsung_internet", { userAgent: UA.samsung }, "samsung_internet", "25"],
    ["vivaldi", { userAgent: UA.vivaldi }, "vivaldi", "6"],
    ["yandex", { userAgent: UA.yandex }, "yandex", "24"],
    ["duckduckgo", { userAgent: UA.duckduckgo }, "duckduckgo", "5"],
  ];
  it.each(table)("%s", (_name, signals, browser, browserVersion) => {
    expect(detectBrowser(signals)).toEqual({ browser, browserVersion });
  });

  it("reports Brave from the presence of navigator.brave beside a verbatim Chrome User-Agent", () => {
    expect(detectBrowser({ userAgent: UA.chromeWin, brave: true })).toEqual({ browser: "brave", browserVersion: "126" });
    expect(detectBrowser({ userAgent: UA.chromeWin, brave: false }).browser).toBe("chrome");
  });

  it("orders derivatives before bases", () => {
    expect(detectBrowser({ userAgent: UA.edge }).browser).toBe("edge"); // contains Chrome
    expect(detectBrowser({ userAgent: UA.chromeWin }).browser).toBe("chrome"); // contains Safari
    expect(detectBrowser({ userAgent: UA.samsung }).browser).toBe("samsung_internet"); // contains Chrome
  });

  it("reads brands after brave and before the User-Agent", () => {
    const brands = [{ brand: "Microsoft Edge", version: "126.0.2592.87" }, { brand: "Chromium", version: "126.0.0.0" }];
    expect(detectBrowser({ userAgent: UA.chromeWin, brands })).toEqual({ browser: "edge", browserVersion: "126" });
    expect(detectBrowser({ userAgent: UA.chromeWin, brands, brave: true }).browser).toBe("brave");
    expect(detectBrowser({ brands: [{ brand: "Google Chrome", version: "126.0.0.0" }] })).toEqual({ browser: "chrome", browserVersion: "126" });
  });

  it("takes Safari's version from Version/, not Safari/", () => {
    expect(detectBrowser({ userAgent: UA.safariIphone }).browserVersion).toBe("17");
  });

  it("is other for an unrecognised User-Agent and unknown for none", () => {
    expect(detectBrowser({ userAgent: UA.unknownThing })).toEqual({ browser: "other", browserVersion: "" });
    expect(detectBrowser({})).toEqual({ browser: "unknown", browserVersion: "" });
  });
});

describe("detectDevice", () => {
  const table: [string, ClientSignals, string][] = [
    ["desktop", { userAgent: UA.chromeWin }, "desktop"],
    ["mobile", { userAgent: UA.safariIphone }, "mobile"],
    ["mobile android", { userAgent: UA.chromeAndroid }, "mobile"],
    ["tablet", { userAgent: UA.safariIpad }, "tablet"],
    ["xr", { userAgent: UA.quest }, "xr"],
    ["console is other", { userAgent: UA.playstation }, "other"],
    ["tv is other", { userAgent: UA.tizen }, "other"],
  ];
  it.each(table)("%s", (_name, signals, device) => {
    expect(detectDevice(signals)).toEqual({ device });
  });

  it("reports a Quest as xr while its OS is still android", () => {
    expect(detectDevice({ userAgent: UA.quest }).device).toBe("xr");
    expect(detectOS({ userAgent: UA.quest }).os).toBe("android");
  });

  it("does not let userAgentData.mobile override a tablet hit", () => {
    expect(detectDevice({ userAgent: UA.safariIpad, mobile: true }).device).toBe("tablet");
    expect(detectDevice({ userAgent: UA.androidTablet, mobile: true }).device).toBe("mobile");
    expect(detectDevice({ userAgent: UA.androidTablet }).device).toBe("desktop"); // no marker: the honest default
  });

  it("keeps OS and device consistent for iPadOS in desktop mode", () => {
    const s = { userAgent: UA.safariMac, platform: "MacIntel", maxTouchPoints: 5 };
    expect(detectOS(s).os).toBe("ipados");
    expect(detectDevice(s).device).toBe("tablet");
  });

  it("is desktop for an unrecognised User-Agent and unknown for none", () => {
    expect(detectDevice({ userAgent: UA.unknownThing }).device).toBe("desktop");
    expect(detectDevice({}).device).toBe("unknown");
  });

  it("never returns wearable", () => {
    for (const ua of Object.values(UA)) {
      expect(detectDevice({ userAgent: ua }).device).not.toBe("wearable");
    }
  });
});

describe("signals are the whole input", () => {
  it("consults only what was supplied", () => {
    // jsdom's navigator would say linux/other/desktop; a supplied object
    // with no userAgent must not be backfilled from it.
    expect(detectOS({ maxTouchPoints: 5 }).os).toBe("unknown");
    expect(detectBrowser({ brave: true }).browser).toBe("brave");
  });
});
