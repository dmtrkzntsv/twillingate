// Bundle entry: register the SDK at window.twillingate and auto-init in
// snippet mode (a data-key on the loading <script> tag). The tag's
// attributes are the only thing read off document.currentScript; the
// collector's origin is baked into the file (origin.ts).
import { bootstrap, type TwillingateGlobal } from "./factory";

declare global {
  interface Window {
    twillingate: TwillingateGlobal;
  }
}

bootstrap(document.currentScript as HTMLScriptElement | null);
