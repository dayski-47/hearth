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
          codemirror: [
            "@codemirror/state",
            "@codemirror/view",
            "@codemirror/commands",
            "@codemirror/language",
            "@codemirror/theme-one-dark",
          ],
        },
        // The CodeMirror language packs are pulled in only by dynamic import()
        // in CodeMirrorHost, so Rollup already splits each into its own async
        // chunk; give those chunks a readable `lang-*` name (their source
        // module is a bare `index.js`, which would otherwise collide).
        chunkFileNames(info) {
          const id = info.facadeModuleId ?? "";
          const pack = id.match(/@codemirror\/lang-([a-z]+)/);
          if (pack) return `assets/lang-${pack[1]}-[hash].js`;
          const legacy = id.match(/@codemirror\/legacy-modes\/mode\/([a-z]+)/);
          if (legacy) return `assets/lang-${legacy[1]}-[hash].js`;
          return "assets/[name]-[hash].js";
        },
      },
    },
  },
});
