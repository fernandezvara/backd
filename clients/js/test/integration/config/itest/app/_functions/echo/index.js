// Used by the JS client's integration tests (roadmap F14): doubles
// ctx.input.n, or throws a function error when asked to, so the tests
// can prove both a successful call and a function's own error arrive
// through the client the way the docs say they do.
export default (ctx) => {
  if (ctx.input && ctx.input.fail) {
    throw ctx.error(409, 'out_of_stock', 'not enough stock')
  }
  return { doubled: ctx.input.n * 2 }
}
