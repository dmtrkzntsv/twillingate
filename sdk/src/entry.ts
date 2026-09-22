// Bundle entry: register the SDK on window (twillingate, or the name in
// data-instance) and auto-init in snippet mode (a data-key on the loading
// <script> tag).
import { Twillingate, bootstrap } from "./twillingate";

declare global {
  interface Window {
    twillingate: Twillingate & { VERSION: string };
  }
}

bootstrap(document.currentScript as HTMLScriptElement | null);
