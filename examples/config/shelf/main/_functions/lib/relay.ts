// ctx.db and ctx.admin.db throw the JavaScript client's own errors
// (NotFoundError, ForbiddenError, VersionMismatchError, ...) when a call
// fails. Uncaught, those become a generic `500 function_failed`: only what a
// function throws with ctx.error(...) reaches its caller with a meaningful
// status. relay() re-throws a caught client error as the equivalent
// ctx.error, so the caller sees exactly what they'd get calling the
// collection themselves (most often a 404 or a 409).
export function relay(err: unknown, error: (status: number, code: string, message: string) => Error): never {
  const e = err as { status?: number; code?: string; message?: string };
  throw error(e.status ?? 500, e.code ?? "function_failed", e.message ?? "unexpected error");
}
