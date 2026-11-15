import { describe, expect, it } from 'vitest';
import catalog from '../../../shared/catalog.json';
import { cartTotals, changeQuantity, isCartMap, nextStatuses } from './domain';
import type { Store } from './types';

const store: Store = catalog[0];

describe('bag and checkout estimates', () => {
  it('uses integer cents and clears the final removed item', () => {
    const cart = changeQuantity(null, store.id, store.menu[0].id, 2);
    expect(cartTotals(cart, store)).toEqual({
      subtotalCents: 3390, taxCents: 339, deliveryFeeCents: 199, serviceFeeCents: 149, totalCents: 4077,
    });
    expect(changeQuantity(cart, store.id, store.menu[0].id, -2)).toBeNull();
    expect(cartTotals(null, store).totalCents).toBe(0);
  });
  it('prevents silently mixing restaurants or exceeding quantity limits', () => {
    const cart = changeQuantity(null, store.id, store.menu[0].id, 20);
    expect(() => changeQuantity(cart, 'other', 'item', 1)).toThrow('another kitchen');
    expect(() => changeQuantity(cart, store.id, store.menu[0].id, 1)).toThrow('20');
  });
  it('rejects malformed saved cart quantities', () => {
    expect(isCartMap({ user: { storeId: 'hearth', items: { x: 2 } } })).toBe(true);
    expect(isCartMap({ user: { storeId: 'hearth', items: { x: '2' } } })).toBe(false);
    expect(isCartMap({ user: { storeId: 'hearth', items: { x: 21 } } })).toBe(false);
  });
});

describe('role actions', () => {
  it('does not offer courier pickup until claimed or allow customer late cancellation', () => {
    expect(nextStatuses('courier', 'ready', false)).toEqual([]);
    expect(nextStatuses('courier', 'ready', true)).toEqual(['picked_up']);
    expect(nextStatuses('customer', 'preparing', false)).toEqual([]);
    expect(nextStatuses('merchant', 'picked_up', false)).toEqual([]);
  });
});
