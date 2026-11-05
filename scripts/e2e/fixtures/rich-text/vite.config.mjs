import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const web = fileURLToPath(new URL("../../../../web/", import.meta.url));
const repository = fileURLToPath(new URL("../../../../", import.meta.url));
const require = createRequire(new URL("../../../../web/package.json", import.meta.url));
const { svelte } = await import(require.resolve("@sveltejs/vite-plugin-svelte"));
const { default: tailwindcss } = await import(require.resolve("@tailwindcss/vite"));

export default {
  root: web,
  plugins: [svelte(), tailwindcss()],
  resolve: { conditions: ["browser"], dedupe: ["svelte"] },
  server: { host: "127.0.0.1", fs: { allow: [repository] } }
};
