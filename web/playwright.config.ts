import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  // The golden path drives a full compose stack plus a real workspace
  // container; on a loaded host the first boot and each podman step are slow.
  timeout: 120_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL: process.env.HEARTH_E2E_URL ?? "http://localhost:8080",
    ignoreHTTPSErrors: true,
  },
  reporter: "list",
  workers: 1,
});
