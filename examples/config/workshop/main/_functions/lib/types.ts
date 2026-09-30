// The parts of a function's context these examples use, typed so the code
// reads well in an editor. `backd functions types` prints the input and
// output types generated from your own schemas.

export type Doc = Record<string, unknown> & {
  id: string;
  _meta: { version: number; created_at: string; owner: string | null };
};

export type ListParams = { where?: object; orderBy?: string; limit?: number; skip?: number };

export type Collection = {
  get(id: string): Promise<Doc>;
  create(doc: Record<string, unknown>): Promise<Doc>;
  patch(id: string, patch: Record<string, unknown>): Promise<Doc>;
  list(params?: ListParams): Promise<{ items: Doc[]; has_more: boolean }>;
  iterate(params?: ListParams): AsyncIterable<Doc>;
};

export type BatchOperation =
  | { op: "create"; collection: string; document: Record<string, unknown> }
  | { op: "patch"; collection: string; id: string; patch: Record<string, unknown>; ifMatch?: number }
  | { op: "delete"; collection: string; id: string };

export type Database = {
  collection(name: string): Collection;
  batch(operations: BatchOperation[]): Promise<Doc[]>;
};

export type Context = {
  input: unknown;
  user: { id: string; email: string; roles: string[] } | null;
  requestId: string;
  idempotencyKey: string | null;
  secrets: Record<string, string>;
  /** Webhook functions only: the raw request. */
  request: { body: string; headers: Record<string, string> };
  db(name: string): Database;
  /** Present for `admin: true` functions, and always for scheduled runs. */
  admin: { db(name: string): Database };
  error(status: number, code: string, message: string): Error;
};
