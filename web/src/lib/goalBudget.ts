/** Guard existing Goals without letting terminal history block subsequent work. */
export function goalBudgetExhausted(goal: {
  status: string;
  token_budget: number | null;
  tokens_used: number;
} | null | undefined): boolean {
  if (!goal || goal.status === "completed" || goal.status === "cancelled") return false;
  return goal.status === "budget_reached" ||
    (goal.token_budget !== null && goal.tokens_used >= goal.token_budget);
}
