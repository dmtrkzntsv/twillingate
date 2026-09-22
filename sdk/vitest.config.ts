import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "jsdom",
    environmentOptions: {
      // A real hostname, so $host assertions read example.com.
      jsdom: { url: "https://example.com/start" },
    },
  },
});
