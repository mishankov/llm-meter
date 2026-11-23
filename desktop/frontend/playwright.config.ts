import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./test",
  use: { browserName: "webkit", viewport: { width: 720, height: 580 }, baseURL: "http://127.0.0.1:4295" },
  webServer: {
    command: "npm run dev -- --port 4295 --strictPort",
    url: "http://127.0.0.1:4295",
  },
});
