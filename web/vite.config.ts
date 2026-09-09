import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The gateway (or the compose stack) the dev server proxies to.
const target = process.env.HEARTH_DEV_TARGET ?? "http://localhost:8080";

// The gateway checks the WebSocket Origin against HEARTH_ALLOWED_ORIGIN. In dev
// the browser's Origin is the Vite server (localhost:5173), so rewrite it to the
// proxy target on every proxied request — HTTP and WS — so no gateway env change
// is needed to develop against the compose stack.
function rewriteOrigin(proxyReq: { setHeader(k: string, v: string): void }) {
  proxyReq.setHeader("origin", target);
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target,
        changeOrigin: true,
        ws: true,
        configure: (proxy) => {
          proxy.on("proxyReq", rewriteOrigin);
          proxy.on("proxyReqWs", rewriteOrigin);
        },
      },
      "/healthz": { target, changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          react: ["react", "react-dom", "react-router-dom"],
          xterm: ["@xterm/xterm", "@xterm/addon-fit"],
        },
      },
    },
  },
});
