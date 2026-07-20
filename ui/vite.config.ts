import { defineConfig } from "vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

// Stratt UI (ADR-0090). File-based routing: the tanstackRouter plugin generates src/routeTree.gen.ts
// from src/routes/**, and autoCodeSplitting lazy-loads each route's component into its own chunk.
// The plugin MUST precede @vitejs/plugin-react. Vite dev proxies /api → strattd (:8080); build
// output ui/dist is served by strattd via STRATT_UI_DIR.
export default defineConfig({
  plugins: [tanstackRouter({ target: "react", autoCodeSplitting: true }), react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  worker: { format: "es" },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://localhost:8080", changeOrigin: true },
    },
  },
});
