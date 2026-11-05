import { defineConfig } from "@playwright/test";
import { readFileSync } from "node:fs";

const manifests = (process.env.TEAMVA_SHOWCASE_MANIFESTS || process.env.TEAMVA_SHOWCASE_MANIFEST || "").split(",").filter(Boolean);
if (!manifests.length) throw new Error("Set TEAMVA_SHOWCASE_MANIFEST(S) to the isolated seeds' showcase.json paths.");
const demos = manifests.map((path) => JSON.parse(readFileSync(path, "utf8")));
if (new Set(demos.map((demo) => demo.locale)).size !== demos.length) throw new Error("Capture each locale only once.");
for (const { origin, locale } of demos) {
  const url = new URL(origin);
  if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || !["en", "zh"].includes(locale)) {
    throw new Error("Only isolated loopback showcases with locale en|zh are allowed.");
  }
}
export default defineConfig({
  testDir: ".", testMatch: "capture.spec.mjs", workers: 1, timeout: 90_000,
  outputDir: "../../web/test-results/showcase", reporter: "list",
  projects: demos.map((demo) => ({ name: demo.locale, metadata: { demo },
    use: { baseURL: demo.origin, locale: demo.locale === "zh" ? "zh-CN" : "en-US" } })),
  use: { viewport: { width: 1600, height: 1100 }, deviceScaleFactor: 2,
    trace: "off", video: "off", screenshot: "off",
    launchOptions: { args: ["--no-proxy-server"], executablePath: process.env.TEAMVA_CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" } }
});
