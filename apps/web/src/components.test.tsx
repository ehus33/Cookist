import { fireEvent, render, screen, waitFor, cleanup } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Checkout } from './components';
import catalog from '../../../shared/catalog.json';
import type { Session, Store } from './types';

const store: Store = catalog[0];
const session: Session = { token: 'demo', role: 'customer', actorId: 'customer-1' };
const props = {
  cart: { storeId: store.id, items: { [store.menu[0].id]: 1 } }, store, session,
  address: '1000 4th Ave Seattle', onAddress: vi.fn(), onQuantity: vi.fn(),
  onClear: vi.fn(), onLogin: vi.fn(), onSuccess: vi.fn(),
};
afterEach(() => { cleanup(); vi.unstubAllGlobals(); sessionStorage.clear(); });

describe('checkout requests', () => {
  it('reuses the idempotency key after an uncertain network failure', async () => {
    const fetch = vi.fn().mockRejectedValue(new Error('Network disconnected'));
    vi.stubGlobal('fetch', fetch);
    const firstRender = render(<Checkout {...props} />);
    fireEvent.click(screen.getByRole('button', { name: /Place demo order/ }));
    await screen.findByRole('alert');
    firstRender.unmount();
    render(<Checkout {...props} />);
    fireEvent.click(screen.getByRole('button', { name: /Place demo order/ }));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    const first = JSON.parse(fetch.mock.calls[0][1].body);
    const second = JSON.parse(fetch.mock.calls[1][1].body);
    expect(first.idempotencyKey).toBe(second.idempotencyKey);
    expect(first.items).toEqual([{ menuItemId: store.menu[0].id, quantity: 1 }]);
    expect(first).not.toHaveProperty('totalCents');
  });
  it('uses a fresh key when the payment method changes after a decline', async () => {
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(new Response(
      JSON.stringify({ error: { code: 'payment_declined', message: 'Declined' } }), { status: 402 },
    )));
    vi.stubGlobal('fetch', fetch);
    render(<Checkout {...props} />);
    fireEvent.change(screen.getByLabelText('Simulated payment'), { target: { value: 'demo_decline' } });
    fireEvent.click(screen.getByRole('button', { name: /Place demo order/ }));
    await screen.findByRole('alert');
    fireEvent.change(screen.getByLabelText('Simulated payment'), { target: { value: 'demo_card' } });
    fireEvent.click(screen.getByRole('button', { name: /Place demo order/ }));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect(JSON.parse(fetch.mock.calls[0][1].body).idempotencyKey)
      .not.toBe(JSON.parse(fetch.mock.calls[1][1].body).idempotencyKey);
  });
  it('shows a service decline without clearing the bag', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: { code: 'payment_declined', message: 'Demo payment declined.' } }),
      { status: 402 },
    )));
    render(<Checkout {...props} />);
    fireEvent.click(screen.getByRole('button', { name: /Place demo order/ }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Demo payment declined.');
    expect(props.onClear).not.toHaveBeenCalled();
    expect(props.onSuccess).not.toHaveBeenCalled();
  });
});
