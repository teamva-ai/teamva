import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";

const port = Number(process.env.TEAMVA_MODAL_PORT || 48079);
if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error("Invalid isolated Modal host port");
const engine = process.env.TEAMVA_MODAL_ENGINE || "chromium";
if (!["chromium", "webkit"].includes(engine)) throw new Error("TEAMVA_MODAL_ENGINE must be chromium or webkit");
const origin = `http://127.0.0.1:${port}`;
const hostConfig = fileURLToPath(new URL("./fixtures/modal/vite.config.mjs", import.meta.url));
export default defineConfig({
  testDir: fileURLToPath(new URL("./", import.meta.url)),
  testMatch: "modal-geometry.spec.mjs",
  outputDir: fileURLToPath(new URL(`../../web/test-results/modal-geometry${engine === "webkit" ? "-webkit" : ""}`, import.meta.url)),
  workers: 1, fullyParallel: false, timeout: 30_000, reporter: "list",
  webServer: {
    cwd: fileURLToPath(new URL("../../", import.meta.url)),
    command: `pnpm --filter @teamva/web exec vite --config "${hostConfig}" --host 127.0.0.1 --port ${port} --strictPort`,
    url: origin, reuseExistingServer: false, timeout: 30_000,
    gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 }
  },
  use: {
    baseURL: origin, browserName: engine, locale: "zh-CN", trace: "off", video: "off", screenshot: "only-on-failure",
    launchOptions: engine === "chromium" ? {
      executablePath: process.env.TEAMVA_CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
      args: ["--no-proxy-server"]
    } : {}
  }
});
