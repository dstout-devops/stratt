import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Dev proxy: the UI is a pure client of /api/v1 (charter §1.6 — same API as
// CLI/CI/agents). Proxying keeps dev CORS-free; production serves the built
// app from strattd (STRATT_UI_DIR).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://localhost:8080", changeOrigin: false },
    },
  },
});
