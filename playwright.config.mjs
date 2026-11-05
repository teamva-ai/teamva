import { readFileSync } from "node:fs";
import { defineConfig } from "@playwright/test";

const stateFile = process.env.TEAMVA_LOCAL_STATE_FILE;
if (!stateFile) throw new Error("请用隔离数据目录启动本地服务，并设置 TEAMVA_LOCAL_STATE_FILE。");
const { origin } = JSON.parse(readFileSync(stateFile, "utf8"));
const target = new URL(origin);
if (target.protocol !== "http:" || !["127.0.0.1", "[::1]"].includes(target.hostname)) {
  throw new Error("浏览器验收只允许隔离的 loopback 服务。");
}

export default defineConfig({
  testDir: "./scripts/e2e",
  globalSetup: "./scripts/e2e/feature-tour-test-setup.mjs",
  testMatch: ["browser-review.spec.mjs", "appearance.spec.mjs", "schedules.spec.mjs", "field-layout.spec.mjs", "default-project.spec.mjs", "input-focus.spec.mjs", "mcp.spec.mjs", "workbench-polish.spec.mjs", "issue-deeplink.spec.mjs", "issue-stream-recovery.spec.mjs", "conversation-fork.spec.mjs", "resource-url-name.spec.mjs", "feature-tours.spec.mjs", "memory-auto-key.spec.mjs", "auto-title.spec.mjs"],
  outputDir: "./web/test-results",
  workers: 1,
  fullyParallel: false,
  timeout: 60_000,
  reporter: "list",
  use: {
    baseURL: origin,
    locale: "zh-CN",
    viewport: { width: 1280, height: 820 },
    screenshot: "only-on-failure",
    // Traces/HAR contain cookies and CSRF tokens. Never record them.
    trace: "off",
    launchOptions: {
      args: ["--no-proxy-server"],
      executablePath: process.env.TEAMVA_CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    }
  }
});
