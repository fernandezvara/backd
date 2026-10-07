// The parts of a function's context these examples use, typed so the code
// reads well in an editor. `backd functions types` prints the input and
// output types generated from your own schemas.

export type Doc = Record<string, unknown> & {
  id: string;
  _meta: { version: number; created_at: string; owner: string | null };
};

export type ListParams = { where?: object; orderBy?: string; limit?: number; skip?: number };

export type FileDetails = { id: string; name: string; size: number; type: string; sha256: string; uploaded_at: string };

/** The files of one document's file field (`collection.files(id, field)`). */
export type Files = {
  /** Reads a file; without an id, the one a single field holds. */
  get(fileId?: string): Promise<{ file?: FileDetails; bytes(): Promise<Uint8Array>; text(): Promise<string> }>;
  /** Stores a file: replaces a single field's, is added to a multiple field's. Resolves with the document. */
  put(data: Uint8Array | string, options?: { name?: string; type?: string; ifMatch?: number }): Promise<Doc>;
  delete(fileId?: string, options?: { ifMatch?: number }): Promise<Doc>;
  /** A link that works without credentials until it expires. */
  link(fileId?: string): Promise<{ url: string; expires_at: string }>;
};

export type Collection = {
  files(id: string, field: string): Files;
  get(id: string): Promise<Doc>;
  create(doc: Record<string, unknown>): Promise<Doc>;
  patch(id: string, patch: Record<string, unknown>, options?: { ifMatch?: number }): Promise<Doc>;
  delete(id: string): Promise<void>;
  list(params?: ListParams): Promise<{ items: Doc[]; has_more: boolean }>;
  iterate(params?: ListParams): AsyncIterable<Doc>;
};

export type Database = {
  collection(name: string): Collection;
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
  /** Calls a function of this database listed in `calls`: its output (sync) or a job handle (async). */
  call(name: string, input?: unknown, options?: { idempotencyKey?: string }): Promise<any>;
  /** Starts a named step of a long job; the Jobs page and `job.steps` show where it is. */
  step(name: string, options?: { total?: number; message?: string }): void;
  /** How far the current step got (out of its `total`), with an optional message. */
  progress(current: number, message?: string): void;
  /** Present for functions with `email: true`: a custom email through the realm's templates. */
  email: {
    send(message: {
      kind: string;
      to_user?: string;
      to?: string[];
      cc?: string[];
      bcc?: string[];
      data?: Record<string, unknown>;
      locale?: string;
    }): Promise<{ id: string; status: string }>;
  };
  error(status: number, code: string, message: string): Error;
};
