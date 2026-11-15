import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/__workbench/",
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/__workbench/api": "http://127.0.0.1:11500",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
