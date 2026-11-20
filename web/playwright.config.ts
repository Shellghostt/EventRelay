import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30000,
  use: {
    baseURL: "http://127.0.0.1:58084",
    viewport: { width: 1440, height: 1100 },
    trace: "retain-on-failure",
  },
  reporter: "list",
  globalTeardown: "./scripts/browser-teardown.ts",
  webServer: {
    command: "node scripts/browser-server.mjs",
    url: "http://127.0.0.1:58084/healthz",
    timeout: 90000,
    reuseExistingServer: false,
    gracefulShutdown: { signal: "SIGTERM", timeout: 10000 },
  },
});
