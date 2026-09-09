import { configDefaults, defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    // The Playwright golden path lives in e2e/ and must never run under Vitest.
    exclude: [...configDefaults.exclude, "e2e/**"],
  },
});
