import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The portal is served from its own origin — services.city.gov, not
// city.gov/cityconnect — so it gets a separate cookie jar. That is the point of
// the split: script running here has no ambient authority over a staff session
// in the same browser. It therefore lives at the root of its host.
const base = process.env.CC_PORTAL_BASE_PATH ?? "/";

export default defineConfig({
  plugins: [react()],
  base,
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      "@shared": fileURLToPath(new URL("../shared", import.meta.url)),
    },
  },
  build: { outDir: "dist", sourcemap: true },
  server: {
    port: 5175,
    proxy: {
      // In production Apache serves /api on this origin too, so the portal's
      // calls stay same-origin and its cookie never needs SameSite=None.
      "/api": { target: "http://127.0.0.1:4021", changeOrigin: false },
    },
  },
});
