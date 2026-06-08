import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies API + WebSocket calls to the Go backend on :8080.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/auth": "http://localhost:8080",
      "/events": "http://localhost:8080",
      "/checkout": "http://localhost:8080",
      "/webhooks": "http://localhost:8080",
      "/graphql": "http://localhost:8080",
      "/tickets": "http://localhost:8080",
      "/ws": { target: "ws://localhost:8080", ws: true },
    },
  },
});
