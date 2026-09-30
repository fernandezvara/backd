// Shared by every function in this project. ctx.db/ctx.admin.db throw the
// JS client's own error classes (NotFoundError, ForbiddenError, ...) on a
// failed call, same as they would for a caller using the client directly
// — but only what a function throws via ctx.error(...) reaches its own
// caller with a meaningful status; anything else, uncaught, becomes a
// generic 500 function_failed (see the docs, "The ctx object"). relay()
// re-throws a caught client error as the equivalent ctx.error, so a
// caller sees exactly what they'd get calling the collection themselves
// — most often a 404 for a group or expense they're not (or no longer) a
// member of, which is how add_expense, list_expenses, request_settlement
// and balances all enforce fresh, current membership (holes 1 and 2).
export function relay(err: unknown, error: (status: number, code: string, message: string) => Error): never {
  const e = err as { status?: number; code?: string; message?: string };
  throw error(e.status ?? 500, e.code ?? "function_failed", e.message ?? "unexpected error");
}
