import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";
import base from "../../playwright.config.mjs";

// Opt-in, isolated UI fixtures only; never part of the normal real-CLI suite.
if (process.env.TEAMVA_PROVIDER_ANOMALY_FIXTURE !== "1") throw new Error("请显式设置 TEAMVA_PROVIDER_ANOMALY_FIXTURE=1；本套件仅为受控 UI fixture。");
if (new URL(base.use.baseURL).port === "41720") throw new Error("禁止连接 41720；请使用新的隔离数据目录和随机 loopback 端口。");

export default defineConfig({
  ...base,
  testDir: fileURLToPath(new URL(".", import.meta.url)),
  outputDir: fileURLToPath(new URL("../../web/test-results/provider-anomalies", import.meta.url)),
  globalSetup: fileURLToPath(new URL("feature-tour-test-setup.mjs", import.meta.url)),
  testMatch: ["provider-anomalies.spec.mjs"]
});
