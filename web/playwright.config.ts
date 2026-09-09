import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 60_000,
  use: {
    baseURL: process.env.HEARTH_E2E_URL ?? "http://localhost:8080",
    ignoreHTTPSErrors: true,
  },
  reporter: "list",
  workers: 1,
});
