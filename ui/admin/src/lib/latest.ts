/**
 * "The last request wins": a view that loads again while an earlier load is still on its way (the
 * initial list, then a click on Trash) must not show the earlier answer when it arrives late.
 * `begin()` starts a request and returns a function that says whether it is still the latest.
 *
 *     const latest = latestOnly()
 *     async function load() {
 *       const current = latest.begin()
 *       const page = await fetch()
 *       if (!current()) return // a newer load started: this answer is stale
 *       ...
 *     }
 */
export function latestOnly() {
  let n = 0
  return {
    begin(): () => boolean {
      const mine = ++n
      return () => mine === n
    },
  }
}
