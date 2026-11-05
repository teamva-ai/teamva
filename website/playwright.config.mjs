import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests", workers: 1, timeout: 30_000, reporter: "list",
  outputDir: "../web/test-results/website",
  use: { baseURL: "http://127.0.0.1:5174", locale: "en-US", trace: "off", video: "off",
    launchOptions: { executablePath: process.env.TEAMVA_CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", args: ["--no-proxy-server"] } }
});
