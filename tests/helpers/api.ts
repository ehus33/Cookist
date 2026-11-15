import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import Stripe from 'stripe';
import { expect, vi } from 'vitest';
import { createDatabase } from '../../src/db.js';
import { createApp } from '../../src/app.js';

export const adminToken = 'test-admin-token-with-enough-length';
const webhookSecret = 'whsec_test';

export async function createFixture() {
  const db = createDatabase(':memory:');
  const stripe = new Stripe('sk_test_example');
  const createAccount = vi.spyOn(stripe.accounts, 'create').mockResolvedValue({ id: 'acct_test' } as Stripe.Response<Stripe.Account>);
  const retrieveAccount = vi.spyOn(stripe.accounts, 'retrieve')
    .mockResolvedValue({ charges_enabled: true, payouts_enabled: true } as Stripe.Response<Stripe.Account>);
  const createLink = vi.spyOn(stripe.accountLinks, 'create').mockResolvedValue({ url: 'https://connect.stripe.test/link' } as Stripe.Response<Stripe.AccountLink>);
  const createCheckout = vi.spyOn(stripe.checkout.sessions, 'create')
    .mockResolvedValue({ id: 'cs_test_123', url: 'https://checkout.stripe.test/session' } as Stripe.Response<Stripe.Checkout.Session>);
  const app = createApp({ db, stripe, adminToken, appUrl: 'http://localhost:3000', webhookSecret });
  const server = createServer(app);
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;

  async function request(path: string, options: { method?: string; body?: unknown; token?: string; headers?: Record<string, string> } = {}) {
    const response = await fetch(base + path, {
      method: options.method || 'GET',
      headers: { ...(options.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...(options.token ? { 'X-Admin-Token': options.token } : {}), ...options.headers },
      ...(options.body !== undefined ? { body: JSON.stringify(options.body) } : {})
    });
    const body = response.headers.get('content-type')?.includes('json') ? await response.json() : await response.text();
    return { status: response.status, body };
  }

  async function addSellerAndMeal() {
    const seller = await request('/api/admin/sellers', {
      method: 'POST', token: adminToken, body: { name: 'Cook', kitchen: 'Shared kitchen', permitReference: 'permit-1' }
    });
    expect(seller.status).toBe(201);
    const meal = await request('/api/admin/meals', { method: 'POST', token: adminToken, body: {
      sellerId: seller.body.id, name: 'Lunch', description: 'Rice and vegetables', allergens: 'Soy',
      pickup: 'Kitchen, Saturday 12pm', priceCents: 1255
    } });
    expect(meal.status).toBe(201);
    return { sellerId: seller.body.id as number, mealId: meal.body.id as number };
  }

  async function webhook(event: object, valid = true) {
    const payload = JSON.stringify(event);
    const signature = valid ? stripe.webhooks.generateTestHeaderString({ payload, secret: webhookSecret }) : 'invalid';
    const response = await fetch(`${base}/api/webhook`, {
      method: 'POST', headers: { 'Content-Type': 'application/json', 'Stripe-Signature': signature }, body: payload
    });
    return response.status;
  }

  async function close() {
    await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    db.close();
  }
  return { db, request, webhook, close, addSellerAndMeal, createAccount, retrieveAccount, createLink, createCheckout };
}
