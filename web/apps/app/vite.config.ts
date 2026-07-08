import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // Connect-RPC + SSE to the Go api service in dev.
      "/iris.v1": { target: "http://localhost:8280", changeOrigin: true },
      "/events": { target: "http://localhost:8280", changeOrigin: true },
    },
  },
});
