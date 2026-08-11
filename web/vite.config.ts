import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The SPA is served by Apache under /cityconnect/, which reverse-proxies
// /cityconnect/api to the Go service. The base path is injected at build time
// so the same source builds for a subpath deployment and for local dev at the
// root.
const base = process.env.CC_BASE_PATH ?? "/cityconnect/";

export default defineConfig({
  plugins: [react()],
  base,
  resolve: {
    // Mirrors the tsconfig paths entry, so `@/…` resolves at build time as
    // well as in the type checker.
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
    // A modest manual split: the vendor chunk changes rarely, so a release
    // that only touches application code leaves it cached.
    rollupOptions: {
      output: {
        manualChunks: {
          vendor: ["react", "react-dom", "react-router-dom", "@tanstack/react-query"],
        },
      },
    },
  },
  server: {
    port: 5174,
    proxy: {
      // In dev the SPA talks to the Go service directly. Note this is port
      // 4021, CityConnect's API — not C2's portal origin on 5173.
      "/api": {
        target: "http://localhost:4021",
        changeOrigin: false,
      },
    },
  },
});
