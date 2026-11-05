import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

window.scrollTo = vi.fn();
// jsdom 不实现 scrollIntoView；IssuePanel 的搜索定位 effect 会在异步时序里
// 调用它，缺失时会产生 unhandled rejection 并让 vitest 以 exit 1 结束
//（即使 159 个测试全部通过）。这里统一打桩，保证测试进程干净退出。
Element.prototype.scrollIntoView = vi.fn();
// jsdom 也不实现 Pointer Events 的捕获 API，图对话的拖动与缩放需要它们。
Element.prototype.setPointerCapture = vi.fn();
Element.prototype.releasePointerCapture = vi.fn();
Element.prototype.hasPointerCapture = vi.fn(() => false);
