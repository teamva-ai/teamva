import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";

const port = Number(process.env.TEAMVA_RICH_TEXT_PORT || 48078);
if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error("Invalid isolated RichText host port");
const origin = `http://127.0.0.1:${port}`;
const hostConfig = fileURLToPath(new URL("./fixtures/rich-text/vite.config.mjs", import.meta.url));
const repository = fileURLToPath(new URL("../../", import.meta.url));

export default defineConfig({
  testDir: fileURLToPath(new URL("./", import.meta.url)),
  testMatch: "rich-text-geometry.spec.mjs",
  outputDir: process.env.TEAMVA_RICH_TEXT_OUTPUT || fileURLToPath(new URL("../../web/test-results/rich-text-geometry", import.meta.url)),
  workers: 1,
  fullyParallel: false,
  timeout: 60_000,
  reporter: "list",
  webServer: {
    cwd: repository,
    command: `pnpm --filter @teamva/web exec vite --config "${hostConfig}" --host 127.0.0.1 --port ${port} --strictPort`,
    url: origin,
    reuseExistingServer: false,
    timeout: 30_000,
    gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 }
  },
  use: {
    baseURL: origin,
    locale: "zh-CN",
    trace: "off",
    video: "off",
    screenshot: "only-on-failure",
    launchOptions: {
      executablePath: process.env.TEAMVA_CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
      args: ["--no-proxy-server"]
    }
  }
});
