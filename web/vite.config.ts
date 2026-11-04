import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// The build goes into the Go package that embeds it, served under /ui/.
// In development, `npm run dev` serves the pages at their real addresses
// and proxies the API to a cisid server, by default the one on :8080.
const server = process.env.CISID_DEV_SERVER ?? "http://localhost:8080";

// uiPrefix lets the dev server answer the /ui/ addresses the page and the
// stylesheet name (fonts, the mark) from the public folder, as the Go
// server answers them from the build.
const uiPrefix: Plugin = {
  name: "cisid-ui-prefix",
  configureServer(dev) {
    dev.middlewares.use((req, _res, next) => {
      if (req.url?.startsWith("/ui/")) req.url = req.url.slice("/ui".length);
      next();
    });
  },
};

export default defineConfig(({ command }) => ({
  base: command === "build" ? "/ui/" : "/",
  plugins: [react(), uiPrefix],
  build: {
    outDir: "../internal/web/static",
    emptyOutDir: true,
    assetsDir: "assets",
    // No inline scripts or styles: the server's CSP allows only files.
    assetsInlineLimit: 0,
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": server,
      "/auth": server,
    },
  },
}));
