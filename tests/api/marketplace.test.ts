import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type Stripe from 'stripe';
import { adminToken, createFixture } from '../helpers/api.js';

let fixture: Awaited<ReturnType<typeof createFixture>>;

beforeEach(async () => {
  fixture = await createFixture();
});

afterEach(async () => {
  await fixture.close();
  vi.restoreAllMocks();
});

describe('sellers and meals', () => {
  it('requires an admin token and lists only approved sellers', async () => {
    expect((await fixture.request('/api/admin/sellers')).status).toBe(401);
    expect((await fixture.request('/api/admin/sellers', { token: 'x'.repeat(adminToken.length) })).status).toBe(401);
    const { sellerId, mealId } = await fixture.addSellerAndMeal();
    expect((await fixture.request('/api/meals')).body).toEqual([]);
    expect((await fixture.request('/api/checkout', { method: 'POST', body: { mealId, quantity: 1 } })).status).toBe(404);
    expect((await fixture.request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', token: adminToken, body: { approved: true } })).status).toBe(200);
    expect((await fixture.request('/api/meals')).body).toMatchObject([{ name: 'Lunch', allergens: 'Soy' }]);
    expect((await fixture.request('/api/admin/sellers', { token: adminToken })).body).toHaveLength(1);
    await fixture.request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', token: adminToken, body: { approved: false } });
    expect((await fixture.request('/api/meals')).body).toEqual([]);
  });

  it('validates seller and meal submissions and approval', async () => {
    for (const body of [{ name: '', kitchen: 'Kitchen', permitReference: 'P1' },
      { name: 123, kitchen: 'Kitchen', permitReference: 'P1' },
      { name: 'x'.repeat(201), kitchen: 'Kitchen', permitReference: 'P1' }]) {
      expect((await fixture.request('/api/admin/sellers', { method: 'POST', token: adminToken, body })).status).toBe(400);
    }
    const meal = { sellerId: 1, name: 'Lunch', description: 'Rice', allergens: 'Soy', pickup: 'Saturday', priceCents: 500 };
    for (const body of [{ ...meal, sellerId: '1' }, { ...meal, name: ' ' }, { ...meal, priceCents: 49 },
      { ...meal, priceCents: 1000001 }, { ...meal, priceCents: 1.5 }]) {
      expect((await fixture.request('/api/admin/meals', { method: 'POST', token: adminToken, body })).status).toBe(400);
    }
    expect((await fixture.request('/api/admin/meals', { method: 'POST', token: adminToken, body: meal })).status).toBe(404);
    expect((await fixture.request('/api/admin/sellers/99', { method: 'PATCH', token: adminToken, body: { approved: true } })).status).toBe(404);
    expect((await fixture.request('/api/admin/sellers/99', { method: 'PATCH', token: adminToken, body: { approved: 'true' } })).status).toBe(400);
  });

  it('creates onboarding links only for existing sellers and reports Stripe errors', async () => {
    expect((await fixture.request('/api/admin/sellers/99/onboarding', { method: 'POST', token: adminToken })).status).toBe(404);
    const { sellerId } = await fixture.addSellerAndMeal();
    expect((await fixture.request(`/api/admin/sellers/${sellerId}/onboarding`, { method: 'POST', token: adminToken })).body)
      .toEqual({ url: 'https://connect.stripe.test/link' });
    expect(fixture.createLink).toHaveBeenCalledWith(expect.objectContaining({ account: 'acct_test', type: 'account_onboarding' }));
    vi.spyOn(console, 'error').mockImplementation(() => {});
    fixture.createLink.mockRejectedValueOnce(new Error('Stripe unavailable'));
    expect((await fixture.request(`/api/admin/sellers/${sellerId}/onboarding`, { method: 'POST', token: adminToken })).status).toBe(500);
    fixture.createAccount.mockRejectedValueOnce(new Error('Stripe unavailable'));
    expect((await fixture.request('/api/admin/sellers', { method: 'POST', token: adminToken,
      body: { name: 'Cook', kitchen: 'Kitchen', permitReference: 'P1' } })).status).toBe(500);
  });
});

describe('checkout and payment', () => {
  it('validates quantities and uses the stored price and rounded fee', async () => {
    const { sellerId, mealId } = await fixture.addSellerAndMeal();
    await fixture.request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', token: adminToken, body: { approved: true } });
    for (const body of [{ mealId: '1', quantity: 1 }, { mealId, quantity: 0 }, { mealId, quantity: 11 }, { mealId, quantity: 1.5 }]) {
      expect((await fixture.request('/api/checkout', { method: 'POST', body })).status).toBe(400);
    }
    fixture.retrieveAccount.mockResolvedValueOnce({ charges_enabled: true, payouts_enabled: false } as Stripe.Response<Stripe.Account>);
    expect((await fixture.request('/api/checkout', { method: 'POST', body: { mealId, quantity: 1 } })).status).toBe(409);
    const result = await fixture.request('/api/checkout', { method: 'POST', body: { mealId, quantity: 2, priceCents: 1 } });
    expect(result.body.url).toBe('https://checkout.stripe.test/session');
    expect(fixture.createCheckout).toHaveBeenCalledWith(expect.objectContaining({
      line_items: [expect.objectContaining({ quantity: 2, price_data: expect.objectContaining({ unit_amount: 1255 }) })],
      payment_intent_data: { application_fee_amount: 251, transfer_data: { destination: 'acct_test' } }
    }), expect.anything());
    vi.spyOn(console, 'error').mockImplementation(() => {});
    fixture.retrieveAccount.mockRejectedValueOnce(new Error('Stripe unavailable'));
    expect((await fixture.request('/api/checkout', { method: 'POST', body: { mealId, quantity: 1 } })).status).toBe(500);
  });

  it('records only valid paid webhook events and handles duplicates', async () => {
    const { sellerId, mealId } = await fixture.addSellerAndMeal();
    await fixture.request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', token: adminToken, body: { approved: true } });
    await fixture.request('/api/checkout', { method: 'POST', body: { mealId, quantity: 1 } });
    const orderId = fixture.db.prepare('SELECT id FROM orders').get()?.id;
    expect(fixture.createCheckout).toHaveBeenCalledWith(expect.objectContaining({ metadata: { orderId } }), expect.anything());
    const event = (type: string, paymentStatus: string, sessionId = 'cs_test_123') => ({
      type, data: { object: { id: sessionId, payment_status: paymentStatus, metadata: { orderId }, customer_details: { email: 'buyer@example.test' } } }
    });
    vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(await fixture.webhook(event('checkout.session.completed', 'paid'), false)).toBe(400);
    expect(await fixture.webhook(event('checkout.session.completed', 'unpaid'))).toBe(200);
    expect(await fixture.webhook(event('other.event', 'paid'))).toBe(200);
    expect(await fixture.webhook(event('checkout.session.completed', 'paid', 'cs_other'))).toBe(200);
    expect((await fixture.request('/api/admin/orders', { token: adminToken })).body).toHaveLength(0);
    expect(await fixture.webhook(event('checkout.session.async_payment_succeeded', 'paid'))).toBe(200);
    expect(await fixture.webhook(event('checkout.session.completed', 'paid'))).toBe(200);
    expect((await fixture.request('/api/admin/orders', { token: adminToken })).body)
      .toMatchObject([{ customer_email: 'buyer@example.test', fee_cents: 126, status: 'paid' }]);
  });
});
