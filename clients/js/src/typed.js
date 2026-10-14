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
 * @template {CollectionShape} C
 * @typedef {{
 *   list(params?: TypedListParams<C['where'], C['order_by']>, opts?: RequestOptions): Promise<TypedPage<C['doc']>>,
 *   iterate(params?: Omit<TypedListParams<C['where'], C['order_by']>, 'skip' | 'count'>, opts?: RequestOptions): AsyncGenerator<C['doc'], void, undefined>,
 *   get(id: string, opts?: import('./data.js').GetOptions): Promise<C['doc']>,
 *   create(doc: C extends { create: infer X } ? X : C['input'], opts?: import('./data.js').CreateOptions): Promise<C['doc']>,
 *   replace(id: string, doc: C['input'], opts?: import('./data.js').WriteOptions): Promise<C['doc']>,
 *   patch(id: string, patch: Partial<C['input']>, opts?: import('./data.js').WriteOptions): Promise<C['doc']>,
 *   delete(id: string, opts?: import('./data.js').DeleteOptions): Promise<void>,
 *   inOrg(org: string): TypedCollection<C>,
 *   inTeam(team: string): TypedCollection<C>,
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
 *   collection<C extends keyof R['db'][D]['collections'] & string>(name: C): TypedCollection<R['db'][D]['collections'][C]>,
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
