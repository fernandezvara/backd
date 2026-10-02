// Panel 1: a function that reads as the caller (order_total).
//
// Customers create draft orders straight in the collection (the rules allow
// that and nothing else). The tax rule is not theirs to compute: the server's
// order_total function does, reading the order as the caller, so someone
// else's order is a 404, exactly as reading the collection would be.

/** @param {{ db: any, ACCOUNTS: any[], money: (cents: number) => string }} deps */
export function ordersPanel({ db, ACCOUNTS, money }) {
  const orders = db.collection('orders')
  return {
    orders: [],
    draft: { item: 'Widget', quantity: 1, price: 10 },
    totals: {}, // order id → { subtotal, tax, total } or { error }

    loadOrders() {
      return this.step('List my orders', async () => {
        const page = await orders.list({ orderBy: '-_meta.created_at', limit: 50 })
        this.orders = page.items
        this.remember(this.orders)
      })
    },

    createOrder() {
      return this.step('Create a draft order', async () => {
        const order = await orders.create({
          item: this.draft.item,
          quantity: Number(this.draft.quantity),
          amount: Math.round(Number(this.draft.price) * 100),
          status: 'draft',
        })
        this.orders = [order, ...this.orders]
        this.remember(this.orders)
      })
    },

    deleteOrder(order) {
      return this.step('Delete a draft order', async () => {
        await orders.delete(order.id)
        this.orders = this.orders.filter((o) => o.id !== order.id)
        delete this.totals[order.id]
      })
    },

    orderTotal(order) {
      return this.step(`Ask the server for the total of ${order.id}`, async () => {
        try {
          const t = await db.fn('order_total', { order_id: order.id })
          this.totals = { ...this.totals, [order.id]: t }
        } catch (err) {
          this.totals = { ...this.totals, [order.id]: { error: `${err.status} ${err.code}: ${err.message}` } }
        }
      })
    },

    // The other customer asks for this customer's order: a 404 for them, as if
    // it didn't exist. Signs in as the other one and back, so both sessions show
    // in the inspector.
    askAsOther(order) {
      const me = this.account
      const other = ACCOUNTS.find((a) => a.role === 'customer' && a.key !== me?.key)
      return this.step(`Ask for ${order.id} as ${other.name}`, async () => {
        await this._signIn(other)
        let answer
        try {
          await db.fn('order_total', { order_id: order.id })
          answer = { error: `${other.name} could read it?!` }
        } catch (err) {
          answer = { error: `as ${other.name}: ${err.status} ${err.code}: ${err.message}` }
        }
        await this._signIn(me) // resets the page's state: show the answer after
        this.totals = { ...this.totals, [order.id]: answer }
      })
    },

    money,
  }
}
