import test from 'node:test';
import assert from 'node:assert/strict';
import { createDatabase } from '../dist/db.js';
import { createApp } from '../dist/app.js';

const adminToken = 'test-admin-token-with-enough-length';

async function fixture(t) {
  const db = createDatabase(':memory:');
  let event;
  let checkoutParams;
  let enabled = true;
  const stripe = {
    accounts: {
      create: async () => ({ id: 'acct_test' }),
      retrieve: async () => ({ charges_enabled: enabled, payouts_enabled: enabled })
    },
    accountLinks: { create: async () => ({ url: 'https://connect.stripe.test/link' }) },
    checkout: { sessions: { create: async params => {
      checkoutParams = params;
      return { id: 'cs_test_123', url: 'https://checkout.stripe.test/session' };
    } } },
    webhooks: { constructEvent: (body, signature) => {
      if (signature !== 'valid') throw new Error('Bad signature');
      return event;
    } }
  };
  const app = createApp({ db, stripe, adminToken, appUrl: 'http://localhost:3000', webhookSecret: 'whsec_test' });
  const server = app.listen(0, '127.0.0.1');
  await new Promise(resolve => server.once('listening', resolve));
  t.after(() => { server.close(); db.close(); });
  const base = `http://127.0.0.1:${server.address().port}`;
  async function request(path, { method = 'GET', body, admin = false, headers = {} } = {}) {
    const response = await fetch(base + path, {
      method,
      headers: { ...(body ? { 'Content-Type': 'application/json' } : {}), ...(admin ? { 'X-Admin-Token': adminToken } : {}), ...headers },
      ...(body ? { body: JSON.stringify(body) } : {})
    });
    const type = response.headers.get('content-type');
    return { status: response.status, body: type?.includes('json') ? await response.json() : await response.text() };
  }
  return { db, request, setEvent: value => { event = value; }, setEnabled: value => { enabled = value; }, getCheckout: () => checkoutParams };
}

async function addSellerAndMeal(request) {
  const seller = await request('/api/admin/sellers', {
    method: 'POST', admin: true, body: { name: 'Cook', kitchen: 'Shared kitchen', permitReference: 'permit-1' }
  });
  assert.equal(seller.status, 201);
  const meal = await request('/api/admin/meals', { method: 'POST', admin: true, body: {
    sellerId: seller.body.id, name: 'Lunch', description: 'Rice and vegetables', allergens: 'Soy',
    pickup: 'Kitchen, Saturday 12pm', priceCents: 1255
  } });
  assert.equal(meal.status, 201);
  return { sellerId: seller.body.id, mealId: meal.body.id };
}

test('only approved sellers are listed, and admin routes require authentication', async t => {
  const { request } = await fixture(t);
  assert.equal((await request('/api/admin/sellers')).status, 401);
  const { sellerId, mealId } = await addSellerAndMeal(request);
  assert.deepEqual((await request('/api/meals')).body, []);
  assert.equal((await request('/api/checkout', { method: 'POST', body: { mealId, quantity: 1 } })).status, 404);
  await request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', admin: true, body: { approved: true } });
  const meals = (await request('/api/meals')).body;
  assert.equal(meals.length, 1);
  assert.equal(meals[0].allergens, 'Soy');
  await request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', admin: true, body: { approved: false } });
  assert.deepEqual((await request('/api/meals')).body, []);
});

test('checkout uses stored price and rounds the 10% fee, requiring Stripe readiness', async t => {
  const { request, setEnabled, getCheckout } = await fixture(t);
  const { sellerId, mealId } = await addSellerAndMeal(request);
  await request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', admin: true, body: { approved: true } });
  setEnabled(false);
  assert.equal((await request('/api/checkout', { method: 'POST', body: { mealId, quantity: 2 } })).status, 409);
  setEnabled(true);
  assert.equal((await request('/api/checkout', { method: 'POST', body: { mealId, quantity: 11 } })).status, 400);
  const response = await request('/api/checkout', { method: 'POST', body: { mealId, quantity: 2, priceCents: 1 } });
  assert.equal(response.status, 200);
  assert.equal(getCheckout().line_items[0].price_data.unit_amount, 1255);
  assert.equal(getCheckout().payment_intent_data.application_fee_amount, 251);
  assert.equal(getCheckout().payment_intent_data.transfer_data.destination, 'acct_test');
});

test('only signed paid webhooks record orders and repeated events are harmless', async t => {
  const { request, setEvent, getCheckout } = await fixture(t);
  const { sellerId, mealId } = await addSellerAndMeal(request);
  await request(`/api/admin/sellers/${sellerId}`, { method: 'PATCH', admin: true, body: { approved: true } });
  await request('/api/checkout', { method: 'POST', body: { mealId, quantity: 1 } });
  const orderId = getCheckout().metadata.orderId;
  setEvent({ type: 'checkout.session.completed', data: { object: {
    id: 'cs_test_123', payment_status: 'unpaid', metadata: { orderId }, customer_details: { email: 'buyer@example.test' }
  } } });
  assert.equal((await request('/api/webhook', { method: 'POST', body: {}, headers: { 'stripe-signature': 'invalid' } })).status, 400);
  await request('/api/webhook', { method: 'POST', body: {}, headers: { 'stripe-signature': 'valid' } });
  assert.equal((await request('/api/admin/orders', { admin: true })).body.length, 0);
  setEvent({ type: 'checkout.session.completed', data: { object: {
    id: 'cs_test_123', payment_status: 'paid', metadata: { orderId }, customer_details: { email: 'buyer@example.test' }
  } } });
  await request('/api/webhook', { method: 'POST', body: {}, headers: { 'stripe-signature': 'valid' } });
  await request('/api/webhook', { method: 'POST', body: {}, headers: { 'stripe-signature': 'valid' } });
  const orders = (await request('/api/admin/orders', { admin: true })).body;
  assert.equal(orders.length, 1);
  assert.equal(orders[0].customer_email, 'buyer@example.test');
  assert.equal(orders[0].fee_cents, 126);
});
