import { describe, expect, it } from "vitest";
import { goalBudgetExhausted } from "./goalBudget";

const budgets = [
  { token_budget: null, tokens_used: 419_410, exhausted: false },
  { token_budget: 1_000, tokens_used: 0, exhausted: false },
  { token_budget: 1_000, tokens_used: 999, exhausted: false },
  { token_budget: 1_000, tokens_used: 1_000, exhausted: true },
  { token_budget: 1_000, tokens_used: 1_001, exhausted: true }
];

describe("goalBudgetExhausted", () => {
  it.each([null, undefined])("does not block when the Goal is %s", (goal) => {
    expect(goalBudgetExhausted(goal)).toBe(false);
  });

  describe.each(["active", "paused", "blocked"])("a %s Goal", (status) => {
    it.each(budgets)("guards $tokens_used / $token_budget", ({ exhausted, ...budget }) => {
      expect(goalBudgetExhausted({ status, ...budget })).toBe(exhausted);
    });
  });

  it.each(budgets)("keeps budget_reached blocked even for $tokens_used / $token_budget", ({ exhausted: _, ...budget }) => {
    expect(goalBudgetExhausted({ status: "budget_reached", ...budget })).toBe(true);
  });

  describe.each(["completed", "cancelled"])("a historical %s Goal", (status) => {
    it.each(budgets)("does not block at $tokens_used / $token_budget", ({ exhausted: _, ...budget }) => {
      expect(goalBudgetExhausted({ status, ...budget })).toBe(false);
    });
  });
});
