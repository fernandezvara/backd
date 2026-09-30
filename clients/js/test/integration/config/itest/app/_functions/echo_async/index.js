// The async twin of ../echo/index.js (roadmap F14): same behavior,
// through a worker instead of inline, so the JS client's Job handle
// (status(), wait()) is exercised against a real claim-and-run cycle.
export default (ctx) => {
  if (ctx.input && ctx.input.fail) {
    throw ctx.error(409, 'out_of_stock', 'not enough stock')
  }
  return { doubled: ctx.input.n * 2 }
}
