// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Admin } from '../../src/public/pages/Admin.js';
import { response } from '../helpers/ui.js';

const seller = { id: 1, name: 'Cook', approved: 0, kitchen: 'Kitchen', permit_reference: 'P1' };
const order = { id: 'order-1', quantity: 2, meal_name: 'Lunch', seller: 'Cook', price_cents: 500,
  fee_cents: 100, pickup: 'Saturday', customer_email: null, created_at: '2026-01-01' };

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it('reports an invalid token and does not unlock', async () => {
  vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => response({ error: 'Invalid administrator token.' }, 401)));
  render(<Admin />);
  fireEvent.change(screen.getByLabelText('Admin token'), { target: { value: 'wrong' } });
  fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
  expect(await screen.findByText('Invalid administrator token.')).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'Add seller' })).toBeNull();
  expect(sessionStorage.getItem('cookist-admin-token')).toBeNull();
});

it('unlocks, approves a seller, and locks again', async () => {
  let approved = false;
  const fetchMock = vi.fn(async (url: string, options: RequestInit) => {
    if (url.endsWith('/sellers/1')) {
      expect(JSON.parse(String(options.body))).toEqual({ approved: true });
      approved = true;
      return response({ ok: true });
    }
    if (url.endsWith('/sellers')) return response([{ ...seller, approved: Number(approved) }]);
    return response([]);
  });
  vi.stubGlobal('fetch', fetchMock);
  render(<Admin />);
  fireEvent.change(screen.getByLabelText('Admin token'), { target: { value: 'secret' } });
  fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
  expect(await screen.findByText('Cook — not approved')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Approve seller' }));
  expect(await screen.findByText('Cook — approved')).toBeTruthy();
  expect(new Headers(fetchMock.mock.calls[0][1].headers).get('X-Admin-Token')).toBe('secret');
  fireEvent.click(screen.getByRole('button', { name: 'Lock admin' }));
  await waitFor(() => expect(screen.getByLabelText('Admin token')).toBeTruthy());
  expect(sessionStorage.getItem('cookist-admin-token')).toBeNull();
});

it('restores a saved session, displays orders, and creates a seller and meal', async () => {
  sessionStorage.setItem('cookist-admin-token', 'secret');
  const fetchMock = vi.fn(async (url: string, options: RequestInit) => {
    if (url === '/api/admin/sellers' && options.method === 'POST') return response({ id: 1 }, 201);
    if (url === '/api/admin/sellers') return response([seller]);
    if (url === '/api/admin/orders') return response([order]);
    if (url === '/api/admin/meals') return response({ id: 2 }, 201);
    throw new Error(`Unexpected request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  render(<Admin />);
  expect(await screen.findByText('Cook — not approved')).toBeTruthy();
  expect(screen.getByText('2 × Lunch — Cook')).toBeTruthy();
  expect(screen.getByText(/Customer: email not provided/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText('Seller name'), { target: { value: 'New cook' } });
  fireEvent.change(screen.getByLabelText('Approved kitchen name and address'), { target: { value: 'Kitchen' } });
  fireEvent.change(screen.getByLabelText('Permit reference'), { target: { value: 'P2' } });
  fireEvent.click(screen.getByRole('button', { name: 'Create test seller' }));
  expect(await screen.findByText(/Seller created/)).toBeTruthy();
  const sellerRequest = fetchMock.mock.calls.find(([url, opts]) => url === '/api/admin/sellers' && opts.method === 'POST');
  expect(JSON.parse(String(sellerRequest?.[1].body))).toEqual({ name: 'New cook', kitchen: 'Kitchen', permitReference: 'P2' });
  fireEvent.change(screen.getByLabelText('Meal name'), { target: { value: 'Bowl' } });
  fireEvent.change(screen.getByLabelText('Description and ingredients'), { target: { value: 'Rice' } });
  fireEvent.change(screen.getByLabelText('Allergen information'), { target: { value: 'Soy' } });
  fireEvent.change(screen.getByLabelText('Pickup location, date and time'), { target: { value: 'Saturday' } });
  fireEvent.change(screen.getByLabelText('Price in dollars'), { target: { value: '12.55' } });
  fireEvent.click(screen.getByRole('button', { name: 'Add meal' }));
  expect(await screen.findByText('Meal added.')).toBeTruthy();
  const mealRequest = fetchMock.mock.calls.find(([url]) => url === '/api/admin/meals');
  expect(JSON.parse(String(mealRequest?.[1].body))).toEqual({ sellerId: 1, name: 'Bowl', description: 'Rice',
    allergens: 'Soy', pickup: 'Saturday', priceCents: 1255 });
});

it('creates an onboarding link and displays errors from seller actions', async () => {
  sessionStorage.setItem('cookist-admin-token', 'secret');
  const fetchMock = vi.fn(async (url: string) => {
    if (url.endsWith('/onboarding')) return response({ url: 'https://connect.stripe.test/link' });
    if (url.endsWith('/sellers/1')) return response({ error: 'Approval failed.' }, 500);
    if (url.endsWith('/sellers')) return response([seller]);
    return response([]);
  });
  vi.stubGlobal('fetch', fetchMock);
  render(<Admin />);
  await screen.findByText('Cook — not approved');
  fireEvent.click(screen.getByRole('button', { name: 'Create onboarding link' }));
  const link = await screen.findByRole('link', { name: 'Open or copy the single-use onboarding link' });
  expect(link.getAttribute('href')).toBe('https://connect.stripe.test/link');
  fireEvent.click(screen.getByRole('button', { name: 'Approve seller' }));
  expect(await screen.findByText('Approval failed.')).toBeTruthy();
});

it('clears an invalid stored token when session restoration fails', async () => {
  sessionStorage.setItem('cookist-admin-token', 'expired');
  vi.stubGlobal('fetch', vi.fn(async () => response({ error: 'Expired token.' }, 401)));
  render(<Admin />);
  expect(await screen.findByText('Expired token.')).toBeTruthy();
  expect(sessionStorage.getItem('cookist-admin-token')).toBeNull();
  expect(screen.getByLabelText('Admin token')).toBeTruthy();
});

it('shows errors when creating a seller, meal, or onboarding link fails', async () => {
  sessionStorage.setItem('cookist-admin-token', 'secret');
  const fetchMock = vi.fn(async (url: string, options: RequestInit) => {
    if (options.method === 'POST') return response({ error: 'Request failed safely.' }, 500);
    return response(url.endsWith('/sellers') ? [seller] : []);
  });
  vi.stubGlobal('fetch', fetchMock);
  render(<Admin />);
  await screen.findByText('Cook — not approved');
  fireEvent.submit(screen.getByRole('button', { name: 'Create test seller' }).closest('form')!);
  expect(await screen.findByText('Request failed safely.')).toBeTruthy();
  fireEvent.submit(screen.getByRole('button', { name: 'Add meal' }).closest('form')!);
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url === '/api/admin/meals')).toBe(true));
  fireEvent.click(screen.getByRole('button', { name: 'Create onboarding link' }));
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.endsWith('/onboarding'))).toBe(true));
  expect(screen.getByRole('status').textContent).toBe('Request failed safely.');
});
