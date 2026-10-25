import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies /api to the alpha replica so the workbench can be
// developed against a live 3-replica lab started via the demo command.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/alpha": { target: "http://127.0.0.1:8101", changeOrigin: true, rewrite: (p) => p.replace(/^\/alpha/, "") },
      "/beta": { target: "http://127.0.0.1:8102", changeOrigin: true, rewrite: (p) => p.replace(/^\/beta/, "") },
      "/gamma": { target: "http://127.0.0.1:8103", changeOrigin: true, rewrite: (p) => p.replace(/^\/gamma/, "") },
    },
  },
});
