package typegen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// FunctionsOutput is a functions project's backd.d.ts.
type FunctionsOutput struct {
	Types    string
	Warnings []string
}

const functionsPreamble = `
/** The user a function runs for, or null for an anonymous caller and scheduled runs. */
export interface CallUser {
  id: string;
  email: string;
  roles: string[];
  /** The organization the session belongs to (only in realms with organizations). */
  org?: string;
  /** The user's membership of that organization, by id (only in realms with organizations): the session reaches it and no other. */
  orgs?: Record<string, CallOrg>;
}

/** A membership, as ctx.user.orgs shows it. */
export interface CallOrg {
  name: string;
  /** Effective roles: direct, default and through teams. */
  roles: string[];
  teams: string[];
  /** The team roles that count now, with their windows. */
  team_roles: { team: string; role: string; from?: string; until?: string }[];
  claims: Record<string, unknown>;
  /** False when the organization requires a second factor and this session used none. */
  counts: boolean;
  require_mfa: boolean;
}

export interface OrgRecord { id: string; name: string; slug: string | null; status: "active" | "orphaned" | "deleted"; require_mfa: boolean; sso_required: boolean; claims: Record<string, unknown>; created_at: string }
export interface OrgTeamRoleGrant { team: string; role: string; from?: string; until?: string; granted_by?: string; granted_at: string; active: boolean }
export interface OrgMember { user: string; email?: string; mfa?: boolean; mfa_roles?: string[]; roles: string[]; teams: string[]; team_roles: OrgTeamRoleGrant[]; effective_roles: { role: string; origin: string[] }[]; overlap: string[]; joined_at: string }
/** A member or role holder of one team: roles_in_team are the team roles held there now. */
export interface OrgTeamMember extends OrgMember { in_team: boolean; roles_in_team: string[] }
export interface OrgTeam { id: string; name: string; roles: string[]; created_at: string }
export interface OrgInvitation {
  id: string; binding: "user" | "email" | "none"; status: "pending" | "used" | "revoked" | "expired";
  user?: string; email?: string; roles: string[]; teams: string[]; max_uses: number; uses: number; expires_at: string;
}

/** ctx.admin.orgs: organizations with full access, in realms that have them. */
export interface OrgAdmin {
  create(input: { name: string; owner: string; slug?: string }): Promise<OrgRecord>;
  get(id: string): Promise<OrgRecord>;
  list(options?: { status?: "active" | "orphaned" | "deleted" }): Promise<OrgRecord[]>;
  setClaims(id: string, claims: Record<string, unknown>): Promise<OrgRecord>;
  members(id: string, options?: { limit?: number; after?: string }): Promise<{ items: OrgMember[]; has_more: boolean; next?: string }>;
  addMember(id: string, input: { user: string; roles?: string[] }): Promise<OrgMember>;
  setRoles(id: string, user: string, roles: string[]): Promise<OrgMember>;
  removeMember(id: string, user: string): Promise<void>;
  assignOwner(id: string, user: string): Promise<OrgMember>;
  teams: {
    list(id: string): Promise<OrgTeam[]>;
    create(id: string, input: { name: string; roles?: string[] }): Promise<OrgTeam>;
    update(id: string, team: string, patch: { name?: string; roles?: string[] }): Promise<OrgTeam>;
    delete(id: string, team: string): Promise<void>;
    /** Members of the team and holders of its roles in force now; with role, only its holders. */
    members(id: string, team: string, options?: { role?: string; limit?: number; after?: string }): Promise<{ items: OrgTeamMember[]; has_more: boolean; next?: string }>;
    /** Gives a member a team role in a team, for the window from..until (RFC 3339). */
    grantRole(id: string, team: string, input: { user: string; role: string; from?: string; until?: string }): Promise<OrgTeamMember>;
    /** Replaces the window of a team role the member holds. */
    setRoleWindow(id: string, team: string, input: { user: string; role: string; from?: string; until?: string }): Promise<OrgTeamMember>;
    revokeRole(id: string, team: string, input: { user: string; role: string }): Promise<OrgTeamMember>;
    addMember(id: string, team: string, user: string): Promise<OrgMember>;
    removeMember(id: string, team: string, user: string): Promise<OrgMember>;
  };
  invitations: {
    create(id: string, input: { binding: "user" | "email" | "none"; user?: string; email?: string; roles?: string[]; teams?: string[]; send?: boolean; expires_in?: string; max_uses?: number }): Promise<OrgInvitation & { token: string; link: string | null }>;
    list(id: string): Promise<OrgInvitation[]>;
    delete(id: string, invitation: string): Promise<void>;
  };
}

export interface ListParams<W, O> {
  where?: W | string;
  orderBy?: O | O[];
  limit?: number;
  skip?: number;
  after?: string;
  count?: boolean;
  /** References to return in _expanded: "author(name,avatar),tags". Nothing is expanded without it. */
  expand?: string;
}

/** What a document's references expand to: the selected fields and the id, null when missing or unreadable. */
type ExpandedDoc<DB, N> = DB extends keyof Realm["db"]
  ? N extends keyof Realm["db"][DB]["collections"]
    ? Partial<Part<Realm["db"][DB]["collections"][N], "doc">> & { id: string } & WithExpanded<Part<Realm["db"][DB]["collections"][N], "expand">>
    : never
  : never;
type ExpandedValue<R> = R extends { db: infer DB; collection: infer N; many: infer M }
  ? M extends true
    ? (ExpandedDoc<DB, N> | null)[] | { items: (ExpandedDoc<DB, N> | null)[]; truncated: true }
    : ExpandedDoc<DB, N> | null
  : never;
/** A document of a collection with references has an optional _expanded, filled by expand. */
export type WithExpanded<E> = [E] extends [never] ? unknown : [keyof E] extends [never] ? unknown : { _expanded?: { [K in keyof E]?: ExpandedValue<E[K]> } };

export interface Page<D> {
  items: D[];
  limit: number;
  skip: number;
  has_more: boolean;
  next_cursor?: string;
  /** With count: true, the size of the whole list. */
  total?: number;
}

/** The files of one document's file field. */
export interface FileField<D> {
  /** Reads a file; without an id, the one a single field holds. */
  get(fileId?: string): Promise<{ file?: FileDetails; bytes(): Promise<Uint8Array>; text(): Promise<string> }>;
  /** Stores a file: replaces a single field's, is added to a multiple field's. Resolves with the document. */
  put(data: Uint8Array | string, options?: { name?: string; type?: string; ifMatch?: number }): Promise<D>;
  delete(fileId?: string, options?: { ifMatch?: number }): Promise<D>;
  /** A link that works without credentials until it expires. */
  link(fileId?: string): Promise<{ url: string; expires_at: string }>;
}

/** What an aggregation of a collection may name: its declared group_by fields, those that are dates (and can be bucketed) and its metrics. */
export interface AggregateParams<A extends { group_by: string; dates: string; metrics: string }, W> {
  where?: W | string;
  groupBy?: GroupKey<A> | GroupKey<A>[];
  /** count (the default), count_distinct:<group_by field>, sum, avg, min or max of a metrics field. */
  metrics?: AggregateMetric<A> | AggregateMetric<A>[];
  /** IANA zone of the date buckets; UTC by default. */
  tz?: string;
  /** Group keys and result names, - for descending: "-count". */
  orderBy?: string | string[];
  limit?: number;
  language?: string;
}
type GroupKey<A extends { group_by: string; dates: string }> = A["group_by"] | §${A["dates"]}:${"day" | "week" | "month" | "year"}§;
type AggregateMetric<A extends { group_by: string; metrics: string }> =
  | "count"
  | §count_distinct:${A["group_by"]}§
  | §${"sum" | "avg" | "min" | "max"}:${A["metrics"]}§;
export interface AggregateGroup {
  /** One entry per groupBy field; null for documents without a value. */
  key: Record<string, string | number | boolean | null>;
  count?: number;
  /** The other metrics by result name: sum_total, avg_total (exact decimal strings), min_x, max_x. */
  [metric: string]: unknown;
}
export interface AggregateResult { groups: AggregateGroup[]; total_groups: number }

/** A collection of a database, typed from its schema. */
export interface TypedCollection<D, I, C, W, O, E = {}, A = never> {
  get(id: string, options?: { expand?: string }): Promise<D & WithExpanded<E>>;
  /** Summarizes the documents the caller may read (only for collections that declare aggregate:). */
  aggregate(params?: [A] extends [never] ? never : AggregateParams<A & { group_by: string; dates: string; metrics: string }, W>): Promise<AggregateResult>;
  /** Full-text search, best match first, with the score in _meta.score. Needs a text index. */
  search(text: string, params?: Omit<ListParams<W, never>, "orderBy" | "after">): Promise<Page<D & WithExpanded<E>>>;
  files(id: string, field: string): FileField<D>;
  list(params?: ListParams<W, O>): Promise<Page<D & WithExpanded<E>>>;
  iterate(params?: Omit<ListParams<W, O>, "skip" | "count">): AsyncGenerator<D & WithExpanded<E>, void, undefined>;
  /** org: the organization a document of a collection with organizations is created in. */
  create(doc: C, options?: { idempotencyKey?: string; org?: string }): Promise<D>;
  replace(id: string, doc: I, options?: { ifMatch?: number | string }): Promise<D>;
  patch(id: string, patch: Partial<I>, options?: { ifMatch?: number | string }): Promise<D>;
  delete(id: string, options?: { ifMatch?: number | string }): Promise<void>;
}

type Part<X, K extends string> = X extends { [P in K]: infer V } ? V : never;

export type BatchOperation = {
  op: "create" | "replace" | "patch" | "delete";
  collection: string;
  id?: string;
  document?: Record<string, unknown>;
  patch?: Record<string, unknown>;
  ifMatch?: number | string;
};

/** A database of the realm. */
export interface TypedDatabase<D extends DatabaseName> {
  collection<C extends keyof Realm["db"][D]["collections"] & string>(
    name: C,
  ): TypedCollection<
    Part<Realm["db"][D]["collections"][C], "doc">,
    Part<Realm["db"][D]["collections"][C], "input">,
    Part<Realm["db"][D]["collections"][C], "create">,
    Part<Realm["db"][D]["collections"][C], "where">,
    Part<Realm["db"][D]["collections"][C], "order_by">,
    Part<Realm["db"][D]["collections"][C], "expand">,
    Part<Realm["db"][D]["collections"][C], "aggregate">
  >;
  batch(operations: BatchOperation[], options?: { idempotencyKey?: string }): Promise<unknown[]>;
}

export interface ConfigChange { path: string; old: unknown; new: unknown }
export interface ConfigWriteResult { version: string; changed: boolean; changes: ConfigChange[] }

export type EmailKind = keyof Realm["templates"]["email"] & string;
export type PushKind = keyof Realm["templates"]["push"] & string;

export interface EmailMessage {
  kind: EmailKind;
  to_user?: string;
  to?: string[];
  cc?: string[];
  bcc?: string[];
  data?: Record<string, unknown>;
  locale?: string;
  requires_consent?: string;
  /** The organization the message is about: its organization context (the email.data_hook) applies. */
  org?: string;
}

export interface PushMessage {
  user?: string;
  users?: string[];
  audience?: unknown;
  kind?: PushKind;
  title?: string;
  body?: string;
  data?: Record<string, unknown>;
  wait?: boolean;
  [key: string]: unknown;
}

export type JobHandle = { id: string; status: string; [key: string]: unknown };
`

// GenerateFunctions produces the backd.d.ts of one database's functions project: the realm's
// types plus defineFunction and a context typed per function.
func GenerateFunctions(reg *registry.Registry, realm, database, config string) (*FunctionsOutput, error) {
	rl, ok := reg.Realms[realm]
	if !ok {
		return nil, fmt.Errorf("unknown realm %q", realm)
	}
	db, ok := rl.Databases[database]
	if !ok {
		return nil, fmt.Errorf("realm %q has no database %q", realm, database)
	}
	g := &gen{entries: map[string]string{}, aliases: map[string]string{}}
	body := g.realm(rl, Options{Realm: realm})

	names := []string{}
	if db.Functions != nil {
		names = slices.Sorted(keys(db.Functions.Functions))
	}
	var flags strings.Builder
	flags.WriteString("/** What each function of this database may use. */\nexport interface FunctionFlags {\n")
	for _, n := range names {
		fn := db.Functions.Functions[n]
		secrets := make([]string, len(fn.Secrets))
		for i, s := range fn.Secrets {
			secrets[i] = literal(s.String())
		}
		sec := "never"
		if len(secrets) > 0 {
			sec = strings.Join(secrets, " | ")
		}
		// A function a trigger runs always has ctx.admin: the realm declared the trigger.
		admin := fn.Admin || triggered(rl, database, n)
		fmt.Fprintf(&flags, "  %s: { webhook: %t; admin: %t; orgs: %t; config_write: %t; email: %t; push: %t; secrets: %s };\n",
			member(n), fn.Mode == registry.ModeWebhook, admin, admin && rl.Settings.Organizations != nil, fn.ConfigWrite, fn.Email, fn.Push != nil, sec)
	}
	flags.WriteString("}\n")

	dbLit := literal(database)
	var b strings.Builder
	fmt.Fprintf(&b, "// Generated by `backd types --functions` (generator %s, realm %q, database %q).\n// Do not edit: change the config and generate again.\n\n", Version, realm, database)
	b.WriteString(body + functionsPreamble)
	fmt.Fprintf(&b, `
export type DatabaseName = keyof Realm["db"] & string;

/** The database these functions belong to. */
export type ThisDatabase = %s;

export type FunctionName = keyof Realm["db"][ThisDatabase]["functions"] & string;

%s
type Fn<N extends FunctionName> = Realm["db"][ThisDatabase]["functions"][N];
type Flag<N extends FunctionName, K extends keyof FunctionFlags[N]> = FunctionFlags[N][K];

type ConfigPaths = Realm["config"]["paths"];

/** What a function receives, typed from its schemas and function.yaml. */
export type Context<N extends FunctionName> = {
  input: Fn<N>["input"];
  user: CallUser | null;
  requestId: string;
  idempotencyKey: string | null;
  secrets: Readonly<Record<Flag<N, "secrets">, string>>;
  /** A database of the realm, as the user the function runs for. */
  db<D extends DatabaseName>(name: D): TypedDatabase<D>;
  /** The realm's app configuration, whole and with no read rules. */
  config: {
    get<P extends keyof ConfigPaths & string>(path: P): Promise<ConfigPaths[P]>;
    get(path: string): Promise<unknown>;
    all(): Promise<Realm["config"]["tree"]>;
  };
  /** Calls a function of this database listed in calls: its output, or a job handle for an async one. */
  call<C extends Fn<N>["calls"] & FunctionName>(
    name: C,
    input: Fn<C>["input"],
    options?: { idempotencyKey?: string },
  ): Promise<Fn<C>["mode"] extends "async" ? JobHandle : Fn<C>["output"]>;
  step(name: string, options?: { total?: number; message?: string }): void;
  progress(current: number, message?: string): void;
  error(status: number, code: string, message: string, details?: unknown): Error;
} & (Flag<N, "webhook"> extends true ? { request: { body: string; headers: Readonly<Record<string, string>> } } : unknown)
  & (Flag<N, "admin"> extends true ? { admin: { db<D extends DatabaseName>(name: D): TypedDatabase<D> } } : unknown)
  & (Flag<N, "orgs"> extends true ? { admin: { orgs: OrgAdmin } } : unknown)
  & (Flag<N, "config_write"> extends true
    ? { admin: { config: {
        set(values: Partial<ConfigPaths>): Promise<ConfigWriteResult>;
        reset(paths: (keyof ConfigPaths & string)[]): Promise<ConfigWriteResult>;
        history(limit?: number): Promise<{ version: string; actor: string; kind: string; changes: ConfigChange[] }[]>;
      } } }
    : unknown)
  & (Flag<N, "email"> extends true ? { email: { send(message: EmailMessage): Promise<{ id?: string; status: string }> } } : unknown)
  & (Flag<N, "push"> extends true ? { push: { send(message: PushMessage): Promise<Record<string, unknown>> } } : unknown);

export type Handler<N extends FunctionName> = (ctx: Context<N>) => Fn<N>["output"] | Promise<Fn<N>["output"]>;

/**
 * Declares a function's handler. At runtime it returns the handler unchanged; its name ties it
 * to the types of this database's registry: it must be "<database>/<function>".
 */
export function defineFunction<N extends FunctionName>(name: §${ThisDatabase}/${N}§, handler: Handler<N>): Handler<N>;
`, dbLit, flags.String())
	slices.Sort(g.warnings)
	return &FunctionsOutput{Types: strings.ReplaceAll(b.String(), "§", "`"), Warnings: g.warnings}, nil
}

// triggered reports whether some collection of the realm has a trigger that runs the function.
func triggered(rl *registry.Realm, database, name string) bool {
	ref := database + "/" + name
	for _, db := range rl.Databases {
		for _, c := range db.Collections {
			for _, t := range c.Triggers {
				if t.Function == ref {
					return true
				}
			}
		}
	}
	return false
}
