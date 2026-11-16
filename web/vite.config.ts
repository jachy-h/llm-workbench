import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/_/workbench/",
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/_/workbench/api": "http://127.0.0.1:11500",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
