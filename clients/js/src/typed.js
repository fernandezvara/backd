// Types only: what `createClient<Realm>()` gives. `Realm` is the interface `backd types` generates
// (types.d.ts); everything here is derived from it, and nothing exists at run time.

/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 * @typedef {import('./data.js').Database} Database
 * @typedef {import('./functions.js').Job} Job
 */

/**
 * The shape of a generated `Realm`.
 * @typedef {object} RealmShape
 * @property {string} realm
 * @property {Record<string, DatabaseShape>} db
 * @property {{ tree: any, paths: Record<string, any> }} config
 */

/**
 * @typedef {object} DatabaseShape
 * @property {Record<string, CollectionShape>} collections
 * @property {Record<string, FunctionShape>} functions
 */

/**
 * @typedef {object} CollectionShape
 * @property {any} doc
 * @property {any} input
 * @property {any} [create] The input of a create: with the `id` a collection with `custom_id` requires. Types generated before custom ids have none, and then `input` is used.
 * @property {any} where
 * @property {any} order_by
 * @property {Record<string, RefShape>} [expand] The collection's declared references, by name. Types generated before reference expansion have none.
 * @property {AggregateShape | never} [aggregate] What the collection lets an aggregation use; `never` when it declares none.
 */

/**
 * The fields of a collection's `aggregate:`: those to group by, those of them that are dates (and can
 * be bucketed) and those to measure.
 * @typedef {object} AggregateShape
 * @property {string} group_by
 * @property {string} dates
 * @property {string} metrics
 */

/**
 * Parameters of an aggregation of a collection, with its declared fields.
 * @template {AggregateShape} A
 * @template W
 * @typedef {{
 *   where?: W | string,
 *   groupBy?: A['group_by'] | `${A['dates']}:${'day' | 'week' | 'month' | 'year'}` | Array<A['group_by'] | `${A['dates']}:${'day' | 'week' | 'month' | 'year'}`>,
 *   metrics?: 'count' | `count_distinct:${A['group_by']}` | `${'sum' | 'avg' | 'min' | 'max'}:${A['metrics']}`
 *     | Array<'count' | `count_distinct:${A['group_by']}` | `${'sum' | 'avg' | 'min' | 'max'}:${A['metrics']}`>,
 *   tz?: string,
 *   orderBy?: string | string[],
 *   limit?: number,
 *   language?: string,
 * }} TypedAggregateParams
 */

/**
 * One declared reference: where it leads and whether the source holds many ids.
 * @typedef {object} RefShape
 * @property {string} db
 * @property {string} collection
 * @property {boolean} many
 */

/**
 * What a reference expanded to, typed from the realm: the target's fields (only the selected ones
 * and the `id` come back, so all but `id` are optional), `null` when it is missing or unreadable,
 * an array of those for a `many` reference, or `{ items, truncated: true }` past the limit.
 * @template {RealmShape} R
 * @template {RefShape} X
 * @typedef {X extends { db: infer DB, collection: infer N }
 *   ? DB extends keyof R['db'] & string
 *     ? N extends keyof R['db'][DB]['collections'] & string
 *       ? X['many'] extends true
 *         ? (ExpandedDoc<R, DB, N> | null)[] | { items: (ExpandedDoc<R, DB, N> | null)[], truncated: true }
 *         : ExpandedDoc<R, DB, N> | null
 *       : never
 *     : never
 *   : never } ExpandedValue
 */

/**
 * A referenced document as an expansion returns it, with its own `_expanded` for a second level.
 * @template {RealmShape} R
 * @template {keyof R['db'] & string} DB
 * @template {keyof R['db'][DB]['collections'] & string} N
 * @typedef {Partial<R['db'][DB]['collections'][N]['doc']> & { id: string } & WithExpanded<R, R['db'][DB]['collections'][N]>} ExpandedDoc
 */

/**
 * `_expanded`, for a collection that declares references.
 * @template {RealmShape} R
 * @template {CollectionShape} C
 * @typedef {C extends { expand: infer E extends Record<string, RefShape> }
 *   ? [keyof E] extends [never] ? unknown : { _expanded?: { [K in keyof E]?: ExpandedValue<R, E[K]> } }
 *   : unknown} WithExpanded
 */

/**
 * @typedef {object} FunctionShape
 * @property {any} input
 * @property {any} output
 * @property {string} mode
 * @property {boolean} internal
 */

/**
 * Options of a list: the collection's own `where` and `order_by` types.
 * `expand` names the references to return in `_expanded`: a string, or `expand(...)`.
 * @template W, O
 * @typedef {Omit<import('./data.js').ListParams, 'where' | 'orderBy'> & { where?: W | string, orderBy?: O | O[] }} TypedListParams
 */

/**
 * One page of documents.
 * @template D
 * @typedef {Omit<import('./data.js').Page, 'items'> & { items: D[] }} TypedPage
 */

/**
 * A collection with its documents typed.
 * @template {RealmShape} R
 * @template {CollectionShape} C
 * @typedef {{
 *   list(params?: TypedListParams<C['where'], C['order_by']>, opts?: RequestOptions): Promise<TypedPage<C['doc'] & WithExpanded<R, C>>>,
 *   iterate(params?: Omit<TypedListParams<C['where'], C['order_by']>, 'skip' | 'count'>, opts?: RequestOptions): AsyncGenerator<C['doc'] & WithExpanded<R, C>, void, undefined>,
 *   get(id: string, opts?: import('./data.js').GetOptions): Promise<C['doc'] & WithExpanded<R, C>>,
 *   aggregate(params?: C extends { aggregate: infer A extends AggregateShape } ? ([A] extends [never] ? never : TypedAggregateParams<A, C['where']>) : import('./data.js').AggregateParams, opts?: RequestOptions): Promise<import('./data.js').AggregateResult>,
 *   search(text: string, params?: Omit<TypedListParams<C['where'], never>, 'orderBy' | 'after'>, opts?: RequestOptions): Promise<TypedPage<C['doc'] & WithExpanded<R, C>>>,
 *   create(doc: C extends { create: infer X } ? X : C['input'], opts?: import('./data.js').CreateOptions): Promise<C['doc']>,
 *   replace(id: string, doc: C['input'], opts?: import('./data.js').WriteOptions): Promise<C['doc']>,
 *   patch(id: string, patch: Partial<C['input']>, opts?: import('./data.js').WriteOptions): Promise<C['doc']>,
 *   delete(id: string, opts?: import('./data.js').DeleteOptions): Promise<void>,
 *   inOrg(org: string): TypedCollection<R, C>,
 *   inTeam(team: string): TypedCollection<R, C>,
 * }} TypedCollection
 */

/**
 * The names of the functions a client may call: internal ones have no route.
 * @template {Record<string, FunctionShape>} F
 * @typedef {{ [K in keyof F & string]: F[K]['internal'] extends true ? never : K }[keyof F & string]} CallableFunctions
 */

/**
 * A database with its collections and functions typed.
 * @template {RealmShape} R
 * @template {keyof R['db'] & string} D
 * @typedef {Omit<Database, 'collection' | 'fn'> & {
 *   collection<C extends keyof R['db'][D]['collections'] & string>(name: C): TypedCollection<R, R['db'][D]['collections'][C]>,
 *   fn<F extends CallableFunctions<R['db'][D]['functions']>>(
 *     name: F,
 *     input: R['db'][D]['functions'][F]['input'],
 *     opts?: RequestOptions & { idempotencyKey?: string, respondAsync?: boolean },
 *   ): Promise<R['db'][D]['functions'][F]['mode'] extends 'async' ? Job : R['db'][D]['functions'][F]['output']>,
 * }} TypedDatabase
 */

/**
 * The app configuration with its paths typed.
 * @template {RealmShape} R
 * @typedef {Omit<Client['config'], 'get'> & {
 *   get<P extends keyof R['config']['paths'] & string>(path: P, opts?: RequestOptions & { refresh?: boolean }): R['config']['paths'][P] | undefined,
 * }} TypedConfig
 */

/**
 * A client typed from a realm's generated types.
 * @template {RealmShape} R
 * @typedef {Omit<Client, 'db' | 'config'> & {
 *   db<D extends keyof R['db'] & string>(name: D): TypedDatabase<R, D>,
 *   config: TypedConfig<R>,
 * }} TypedClient
 */

export {}
