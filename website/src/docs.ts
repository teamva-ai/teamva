import "@fontsource/geist-sans/400.css";
import "@fontsource/geist-sans/500.css";
import "@fontsource/geist-sans/600.css";
import { mount } from "svelte";
import DocsApp from "./DocsApp.svelte";
import "./app.css";

const target = document.getElementById("app");
if (!target) throw new Error("Missing #app element");

mount(DocsApp, { target });
