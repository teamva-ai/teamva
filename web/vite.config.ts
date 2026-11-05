import { defineConfig } from "vitest/config";
import { loadEnv } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "TEAMVA_");
  const target = new URL(env.TEAMVA_DEV_API || "http://127.0.0.1:8080");
  if (target.protocol !== "http:" || !["127.0.0.1", "[::1]", "localhost"].includes(target.hostname) ||
      target.username || target.password || target.pathname !== "/" || target.search || target.hash) {
    throw new Error("TEAMVA_DEV_API 必须是本地 loopback HTTP origin");
  }
  return {
  plugins: [svelte(), tailwindcss()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/v1": {
        target: target.origin,
        changeOrigin: true,
        configure(proxy) {
          // The browser talks only to Vite. Forward its own origin as the local
          // API origin, but never turn a foreign-origin request into a trusted one.
          proxy.on("proxyReq", (outgoing, incoming) => {
            if (incoming.headers.origin === `http://${incoming.headers.host}`) {
              outgoing.setHeader("Origin", target.origin);
              outgoing.setHeader("Referer", `${target.origin}/`);
            }
          });
        }
      }
    }
  },
  resolve: {
    conditions: ["browser"]
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    // 组件测试都在 afterEach 清理 query cache/localStorage，不依赖文件级
    // 隔离；每个文件新开 jsdom 环境与重新 transform Svelte/TanStack 是本层
    // 最大的固定开销。固定 4 个 threads worker：worker 太少串行、太多则
    // 环境创建互相争抢，本机实测 22.8s（默认 forks/隔离）→ 15.5s。
    pool: "threads",
    maxWorkers: 4,
    isolate: false
  }
  };
});
