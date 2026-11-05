import base from "../rich-text/vite.config.mjs";
import { fileURLToPath } from "node:url";

export default {
  ...base,
  resolve: { ...base.resolve, dedupe: ["svelte", "@tanstack/svelte-query"] },
  // Optional compile-only check. The browser test serves index.html via /@fs.
  build: { rollupOptions: { input: fileURLToPath(new URL("./main.js", import.meta.url)) } }
};
