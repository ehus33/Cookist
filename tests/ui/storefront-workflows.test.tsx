// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Storefront } from '../../src/public/pages/Storefront.js';
import { response } from '../helpers/ui.js';

afterEach(() => {
  cleanup();
  history.replaceState(null, '', '/');
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it('renders meal details and shows checkout errors', async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(response([{ id: 1, name: 'Lunch', seller: 'Cook', description: 'Rice',
      allergens: 'Soy', pickup: 'Saturday', kitchen: 'Shared kitchen', price_cents: 1255 }]))
    .mockResolvedValueOnce(response({ error: 'Seller is not ready.' }, 409));
  vi.stubGlobal('fetch', fetchMock);
  render(<Storefront />);
  expect(await screen.findByText('Lunch')).toBeTruthy();
  expect(screen.getByText('Allergens: Soy')).toBeTruthy();
  expect(screen.getByText('$12.55')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Test checkout' }));
  expect(await screen.findByRole('status')).toHaveProperty('textContent', 'Seller is not ready.');
  expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ mealId: 1, quantity: 1 });
  expect((screen.getByRole('button', { name: 'Test checkout' }) as HTMLButtonElement).disabled).toBe(false);
});

it('shows an explicit message when the menu cannot load', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')));
  render(<Storefront />);
  expect(await screen.findByText('Could not load the menu.')).toBeTruthy();
});

it.each([
  ['success', /Checkout completed/],
  ['cancelled', /Checkout cancelled/]
])('displays the %s checkout notice and an empty menu', async (status, notice) => {
  history.replaceState(null, '', `/?checkout=${status}`);
  vi.stubGlobal('fetch', vi.fn(async () => response([])));
  render(<Storefront />);
  expect(screen.getByRole('status').textContent).toMatch(notice);
  expect(await screen.findByText(/No meals are listed yet/)).toBeTruthy();
});

it('sends the selected meal to checkout when the request succeeds', async () => {
  const checkoutUrl = new URL('#test-checkout', window.location.href).href;
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(response([{ id: 7, name: 'Soup', seller: 'Cook', description: 'Carrot',
      allergens: 'None', pickup: 'Saturday', kitchen: 'Kitchen', price_cents: 500 }]))
    .mockResolvedValueOnce(response({ url: checkoutUrl }));
  vi.stubGlobal('fetch', fetchMock);
  render(<Storefront />);
  fireEvent.click(await screen.findByRole('button', { name: 'Test checkout' }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ mealId: 7, quantity: 1 });
  await waitFor(() => expect(window.location.hash).toBe('#test-checkout'));
});
