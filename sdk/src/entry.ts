// Bundle entry: expose the SDK as window.twillingate and auto-init in
// snippet mode (a data-key on the loading <script> tag).
import { Twillingate, autoInit, supersededBy, VERSION } from "./twillingate";

declare global {
  interface Window {
    twillingate: Twillingate & { VERSION: string };
  }
}

const script = document.currentScript as HTMLScriptElement | null;

if (!supersededBy(window.twillingate, script)) {
  const tg = new Twillingate();
  (tg as Twillingate & { VERSION: string }).VERSION = VERSION;
  window.twillingate = tg as Twillingate & { VERSION: string };
  autoInit(tg, script);
}
