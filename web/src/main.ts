import { mount } from "svelte";
import Root from "./Root.svelte";
import "./lib/i18n.svelte";
import "./app.css";

const target = document.getElementById("app");
if (!target) {
  throw new Error("Missing #app element");
}

// index.html contains a zero-JavaScript login shell so the first meaningful
// paint is not blocked on the application bundle. Remove it immediately before
// Svelte takes ownership of the root.
target.replaceChildren();
const app = mount(Root, { target });

export default app;
