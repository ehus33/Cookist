import type { Order } from '../types.js';

export function OrderList({ orders }: { orders: Order[] }) {
  return <section className="panel"><h2>Paid test orders</h2><p>Orders appear after a verified Stripe webhook confirms payment.</p>
    {!orders.length && <p>No paid test orders yet.</p>}
    {orders.map(order => <div className="admin-row" key={order.id}>
      <strong>{order.quantity} × {order.meal_name} — {order.seller}</strong>
      <p>Total: ${(order.price_cents * order.quantity / 100).toFixed(2)} · Platform fee: ${(order.fee_cents / 100).toFixed(2)}</p>
      <p>Pickup: {order.pickup} · Customer: {order.customer_email || 'email not provided'}</p>
      <p className="detail">Order {order.id} · {order.created_at} UTC</p>
    </div>)}
  </section>;
}
