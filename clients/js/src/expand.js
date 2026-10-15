/**
 * One entry of an `expand`: a reference name (or `name.nested` for a second level), or the
 * name with the fields to return: `['author', ['name', 'avatar']]`.
 * @typedef {string | [string, string[]]} ExpandPart
 */

const NAME = /^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$/

/**
 * Builds the `expand` option of a read: the references to return next to the result, in
 * `_expanded`, with the fields to return of each. Nothing is expanded unless you pass this (or
 * a string) as `expand`; the client never adds it by itself.
 *
 * ```js
 * const posts = await db.collection('posts').list({
 *   expand: expand(['author', ['name', 'avatar']], 'tags'),   // author(name,avatar),tags
 * })
 * posts.items[0]._expanded?.author        // the profile, or null
 * ```
 * @param {...ExpandPart} parts
 * @returns {string}
 */
export function expand(...parts) {
  return parts
    .map((part) => {
      const [name, fields] = Array.isArray(part) ? part : [part, undefined]
      if (typeof name !== 'string' || !NAME.test(name)) {
        throw new TypeError(`backd: ${JSON.stringify(name)} is not a reference name to expand`)
      }
      if (fields === undefined) return name
      if (!Array.isArray(fields) || fields.length === 0 || !fields.every((f) => typeof f === 'string' && NAME.test(f))) {
        throw new TypeError(`backd: the fields of ${name} must be a non-empty list of field names`)
      }
      return `${name}(${fields.join(',')})`
    })
    .join(',')
}
