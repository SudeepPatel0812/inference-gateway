import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The browser talks only to Vite (localhost:5173); Vite forwards API calls to the gateway.
// Same origin for the browser = no CORS setup needed in the Go gateway.
const gateway = process.env.GATEWAY_URL ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/v1": gateway,
      "/health": gateway,
    },
  },
});
