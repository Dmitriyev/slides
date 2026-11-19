import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  return {
    plugins: [react()],
    server: {
      port: 5173,
      strictPort: true,
      proxy: {
        "/api": {
          target: env.SLIDES_API_URL || "http://128.140.108.182:8280",
          changeOrigin: true,
          headers: env.SLIDES_API_TOKEN
            ? { Authorization: `Bearer ${env.SLIDES_API_TOKEN}` }
            : {},
        },
        "/healthz": {
          target: env.SLIDES_API_URL || "http://128.140.108.182:8280",
          changeOrigin: true,
        },
      },
    },
  };
});
