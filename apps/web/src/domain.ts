import type { Cart, Role, Status, Store, Totals } from './types';

export const statusLabels: Record<Status, string> = {
  pending: 'Waiting for the kitchen', accepted: 'Order accepted', preparing: 'Cooking something good',
  ready: 'Ready for pickup', picked_up: 'On the way', delivered: 'Delivered. Enjoy!', cancelled: 'Cancelled',
};
export const journey: Status[] = ['pending', 'accepted', 'preparing', 'ready', 'picked_up', 'delivered'];
export const money = (cents: number) => new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(cents / 100);

export function cartTotals(cart: Cart | null, store: Store | undefined): Totals {
  const subtotalCents = cart && store ? store.menu.reduce(
    (total, item) => total + item.priceCents * (cart.items[item.id] ?? 0), 0,
  ) : 0;
  const taxCents = Math.round(subtotalCents / 10);
  const deliveryFeeCents = subtotalCents ? store?.deliveryFeeCents ?? 0 : 0;
  const serviceFeeCents = subtotalCents ? 149 : 0;
  return { subtotalCents, taxCents, deliveryFeeCents, serviceFeeCents, totalCents: subtotalCents + taxCents + deliveryFeeCents + serviceFeeCents };
}

export function changeQuantity(cart: Cart | null, storeId: string, itemId: string, delta: number): Cart | null {
  if (cart && cart.storeId !== storeId) throw new Error('Your bag contains food from another kitchen. Clear it before switching.');
  const items = { ...cart?.items };
  const quantity = (items[itemId] ?? 0) + delta;
  if (quantity > 20) throw new Error('You can add up to 20 of each item.');
  if (quantity <= 0) delete items[itemId];
  else items[itemId] = quantity;
  return Object.keys(items).length ? { storeId, items } : null;
}

export function nextStatuses(role: Role, status: Status, claimed: boolean): Status[] {
  if (role === 'customer') return status === 'pending' ? ['cancelled'] : [];
  if (role === 'courier') return !claimed ? [] : status === 'ready' ? ['picked_up'] : status === 'picked_up' ? ['delivered'] : [];
  const steps: Partial<Record<Status, Status[]>> = {
    pending: ['accepted', 'cancelled'], accepted: ['preparing', 'cancelled'],
    preparing: ['ready', 'cancelled'], ready: ['cancelled'],
  };
  return steps[status] ?? [];
}

export function isSession(value: unknown): value is import('./types').Session {
  return typeof value === 'object' && value !== null
    && 'token' in value && typeof value.token === 'string'
    && 'actorId' in value && typeof value.actorId === 'string'
    && 'role' in value && (value.role === 'customer' || value.role === 'merchant' || value.role === 'courier');
}

export function isCartMap(value: unknown): value is Record<string, Cart | null> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    && Object.values(value).every(cart => cart === null || (
      typeof cart === 'object' && 'storeId' in cart && typeof cart.storeId === 'string'
      && 'items' in cart && typeof cart.items === 'object' && cart.items !== null && !Array.isArray(cart.items)
      && Object.values(cart.items).every(qty => Number.isInteger(qty) && Number(qty) >= 1 && Number(qty) <= 20)
    ));
}
