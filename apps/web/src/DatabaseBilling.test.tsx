import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { DatabaseBilling } from './DatabaseBilling';

const session = { token: 'customer-token', actorId: 'customer' };
const plan = {
  configured: true, serviceReady: true, subscribed: false, hasSubscription: false,
  canCheckout: true, canManageBilling: true, mode: 'test', billingModel: 'aws-cost-markup',
  multiplier: '1.9', currency: 'usd', databaseState: 'not_created', canDeleteDatabase: false, invoices: [],
};
const invoice = { period: '2026-10', awsCostUsd: '12.345678', chargeCents: 2346, status: 'paid', url: 'https://invoice.stripe.com/i/signed-invoice' };
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}
async function authorize() {
  const button = await screen.findByRole('button', { name: 'Authorize database billing' });
  fireEvent.click(screen.getByRole('checkbox', { name: /I authorize variable monthly charges/ }));
  fireEvent.click(button);
}
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); window.history.replaceState({}, '', '/'); });

it('requires explicit variable-charge consent, posts empty setup checkout, and waits for verified status even on return', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  window.history.replaceState({}, '', '/?database=success&session_id=untrusted');
  let verified = false;
  const fetch = vi.fn(async (path: string, _options?: RequestInit) => path.endsWith('/checkout')
    ? json({ url: 'https://checkout.stripe.com/c/setup/database' })
    : json(verified ? { ...plan, subscribed: true, hasSubscription: true, canCheckout: false, databaseState: 'retained' } : plan));
  vi.stubGlobal('fetch', fetch);
  render(<DatabaseBilling session={session} />);
  const button = await screen.findByRole('button', { name: 'Authorize database billing' });
  expect(button).toBeDisabled();
  expect(screen.getByRole('checkbox')).not.toBeChecked();
  fireEvent.click(button);
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(screen.getByText(/prior calendar month's actual AWS-tagged DynamoDB cost/)).toHaveTextContent('× 1.9, rounded to cents in USD and invoiced monthly in arrears');
  expect(screen.getByText(/Other AWS services and unattributed charges/)).toBeInTheDocument();
  expect(screen.getByText(/Retained storage continues/)).toBeInTheDocument();
  expect(screen.getByText(/you do not need an AWS account/)).toBeInTheDocument();
  expect(screen.queryByText(/\/ month per organization/)).not.toBeInTheDocument();
  await authorize();
  expect(await screen.findByRole('link', { name: 'Continue to database checkout' })).toHaveAttribute('href', 'https://checkout.stripe.com/c/setup/database');
  expect(screen.getByText(/Stripe setup saves your payment method/)).toHaveTextContent('returning here does not confirm payment or authorize access');
  const request = fetch.mock.calls.find(([path]) => path.endsWith('/checkout'))!;
  expect(request[1]?.method).toBe('POST');
  expect(request[1]?.body).toBeUndefined();
  expect(request[1]?.headers).toEqual(expect.objectContaining({ Authorization: 'Bearer customer-token' }));
  expect(screen.queryByText(/Database billing active/)).not.toBeInTheDocument();
  expect(screen.getByRole('checkbox')).not.toBeChecked();
  verified = true;
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  await screen.findByText(/Database billing active/);
  expect(screen.queryByRole('button', { name: 'Authorize database billing' })).not.toBeInTheDocument();
  expect(screen.queryByRole('link', { name: 'Continue to database checkout' })).not.toBeInTheDocument();
});

it('uses the shared empty portal POST for inactive billing, without altering the Cookist plan', async () => {
  const fetch = vi.fn(async (path: string, _options?: RequestInit) => path.endsWith('/portal')
    ? json({ url: 'https://billing.stripe.com/p/session/database' })
    : json({ ...plan, hasSubscription: true, canCheckout: false, databaseState: 'retained' }));
  vi.stubGlobal('fetch', fetch);
  render(<DatabaseBilling session={session} />);
  await screen.findByText(/authorization is pending or inactive/);
  expect(screen.queryByRole('button', { name: 'Authorize database billing' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Manage database billing' }));
  expect(await screen.findByRole('link', { name: 'Continue to database billing portal' })).toHaveAttribute('href', 'https://billing.stripe.com/p/session/database');
  const request = fetch.mock.calls.find(([path]) => path.endsWith('/portal'))!;
  expect(request[0]).toBe('/api/billing/portal');
  expect(request[1]?.method).toBe('POST');
  expect(request[1]?.body).toBeUndefined();
  expect(screen.getByText(/Cancelling your Cookist plan/)).toHaveTextContent('does not cancel database billing');
});

it('renders actual-cost invoices and treats an unknown invoice status as unknown, not paid', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, invoices: [invoice,
    { ...invoice, period: '2026-09', status: 'adjusted', url: 'https://pay.stripe.com/signed-invoice' }] })));
  render(<DatabaseBilling session={session} />);
  expect(await screen.findAllByText('$12.345678')).toHaveLength(2);
  expect(screen.getAllByText('$23.46')).toHaveLength(2);
  expect(screen.getByText('Paid')).toBeInTheDocument();
  expect(screen.getByText('Unknown invoice status')).toBeInTheDocument();
  expect(screen.queryByText('adjusted')).not.toBeInTheDocument();
  expect(screen.getByText(/Unpaid overstated invoices are voided and replaced/)).toHaveTextContent('reductions to paid amounts become future Stripe customer-balance credits, not cash refunds');
  expect(screen.getByRole('link', { name: 'View invoice for 2026-10' })).toHaveAttribute('href', invoice.url);
  expect(screen.getByRole('link', { name: 'View invoice for 2026-09' })).toHaveAttribute('href', 'https://pay.stripe.com/signed-invoice');
  expect(screen.queryByRole('button', { name: /retry.*charge/i })).not.toBeInTheDocument();
});

it.each([
  { multiplier: '2' }, { currency: 'eur' }, { billingModel: 'estimate' }, { databaseState: 'unknown' },
  { canDeleteDatabase: true }, { subscribed: 'true' }, { mode: 'unknown' }, { invoices: undefined }, { notice: 42 },
  { invoices: [{ ...invoice, awsCostUsd: '--1' }] }, { invoices: [{ ...invoice, awsCostUsd: 'NaN' }] },
  { invoices: [{ ...invoice, awsCostUsd: 12 }] }, { invoices: [{ ...invoice, awsCostUsd: '1e3' }] },
  { invoices: [{ ...invoice, chargeCents: 1.5 }] }, { invoices: [{ ...invoice, chargeCents: Number.MIN_SAFE_INTEGER - 1 }] },
  { invoices: [{ ...invoice, period: '2026-13' }] }, { invoices: [{ ...invoice, status: null }] },
  { invoices: [{ ...invoice, status: '<script>' }] }, { invoices: [{ ...invoice, url: 5 }] },
  { serviceReady: false }, { price: { amountCents: -5, currency: 'usd', interval: 'month' } },
])('rejects malformed status or cost data without inventing prices: %j', async change => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, ...change })));
  render(<DatabaseBilling session={session} />);
  expect(await screen.findByRole('alert')).toHaveTextContent('invalid database billing status or costs');
  expect(screen.queryByRole('button', { name: 'Authorize database billing' })).not.toBeInTheDocument();
  expect(screen.queryByRole('table')).not.toBeInTheDocument();
  expect(screen.queryByText(/\$38/)).not.toBeInTheDocument();
});

it.each([
  'http://invoice.stripe.com/pay/123', 'https://invoice.stripe.com.evil.invalid/pay/123',
  'https://invoice.stripe.com@evil.invalid/pay/123', 'https://user@invoice.stripe.com/pay/123',
  'https://invoice.stripe.com:8443/pay/123', 'https://invoice.stripe.com:443/i/123',
  'https://pay.stripe.com.evil.invalid/123',
  'https://user:secret@pay.stripe.com/123', 'javascript:alert(1)', 'not-a-url',
])('visibly rejects unsafe invoice URLs: %s', async url => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, invoices: [{ ...invoice, url }] })));
  render(<DatabaseBilling session={session} />);
  expect(await screen.findByRole('alert')).toHaveTextContent('unsafe billing link');
  expect(screen.queryByRole('link')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Authorize database billing' })).not.toBeInTheDocument();
});

it('does not show legacy fixed pricing for actual-cost billing, but supports an explicit legacy contract', async () => {
  const fetch = vi.fn(async () => json({ ...plan, price: { amountCents: 3800, currency: 'usd', interval: 'month' } }));
  vi.stubGlobal('fetch', fetch);
  const mounted = render(<DatabaseBilling session={session} />);
  await screen.findByRole('button', { name: 'Authorize database billing' });
  expect(screen.queryByText(/\$38/)).not.toBeInTheDocument();
  fetch.mockImplementation(async () => json({ ...plan, billingModel: 'fixed-monthly', price: { amountCents: 4500, currency: 'usd', interval: 'month' } }));
  mounted.rerender(<DatabaseBilling session={{ ...session, token: 'renewed' }} />);
  await screen.findByText(/\$45.00 \/ month per organization/);
  expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Add database subscription' })).toBeEnabled();
});

it('renders signed actual AWS credits and cumulative charges without inventing another payment action', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, invoices: [{ ...invoice,
    awsCostUsd: '-12.345678', chargeCents: -2346, status: 'paid' }] })));
  render(<DatabaseBilling session={session} />);
  expect(await screen.findByText('-$12.345678')).toBeInTheDocument();
  expect(screen.getByText('-$23.46')).toBeInTheDocument();
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

it('distinguishes posted customer-balance credits from verified zero-charge months', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, invoices: [
    { ...invoice, awsCostUsd: '-12.345678', chargeCents: -2346, status: 'credited', url: undefined },
    { ...invoice, period: '2026-09', awsCostUsd: '0.001', chargeCents: 0, status: 'no_charge', url: undefined },
  ] })));
  render(<DatabaseBilling session={session} />);
  expect(await screen.findByText('Credit posted to Stripe customer balance (not a cash refund)')).toBeInTheDocument();
  expect(screen.getByText('Verified AWS cost rounds to zero — no Stripe charge')).toBeInTheDocument();
  expect(screen.getByText('-$23.46')).toBeInTheDocument();
  expect(screen.getByText('$0.00')).toBeInTheDocument();
  expect(screen.queryByText('Paid')).not.toBeInTheDocument();
  expect(screen.queryByText('Unknown invoice status')).not.toBeInTheDocument();
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

it.each([
  { configured: false, canCheckout: false }, { serviceReady: false, canCheckout: false },
  { canCheckout: false, canManageBilling: false },
])('keeps unavailable billing disabled without fallback prices: %j', async change => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, ...change })));
  render(<DatabaseBilling session={session} />);
  await screen.findByText(/Database: not created/);
  expect(screen.queryByRole('button', { name: 'Authorize database billing' })).not.toBeInTheDocument();
  expect(screen.queryByText(/\/ month per organization/)).not.toBeInTheDocument();
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

it('shows an attribution outage notice while retaining database deletion, billing management, and invoice history', async () => {
  const notice = 'AWS cost attribution is unavailable. New authorizations are paused; data recovery and database deletion remain available.';
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, serviceReady: false, canCheckout: false,
    databaseState: 'retained', canDeleteDatabase: true, notice, invoices: [invoice] })));
  render(<DatabaseBilling session={session} />);
  expect(await screen.findByRole('alert')).toHaveTextContent(notice);
  expect(screen.queryByRole('button', { name: 'Authorize database billing' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Manage database billing' })).toBeEnabled();
  expect(screen.getByRole('link', { name: 'View invoice for 2026-10' })).toHaveAttribute('href', invoice.url);
  fireEvent.click(screen.getByRole('button', { name: 'Delete organization database' }));
  expect(screen.getByRole('group', { name: 'Confirm permanent database deletion' })).toBeInTheDocument();
  expect(screen.getByLabelText('Type DELETE DATABASE to confirm')).toBeEnabled();
  expect(screen.getByRole('alert')).toHaveTextContent(notice);
});

it.each(['checkout', 'portal'])('surfaces errors and blocks unsafe %s redirects', async action => {
  let fail = true;
  const fetch = vi.fn(async (path: string) => fail ? json({ error: { message: 'Database billing is unavailable.' } }, 503)
    : path.endsWith(`/${action}`) ? json({ url: `https://${action === 'portal' ? 'billing' : 'checkout'}.stripe.com@evil.invalid/pay` }) : json(plan));
  vi.stubGlobal('fetch', fetch);
  const mounted = render(<DatabaseBilling session={session} />);
  expect(await screen.findByRole('alert')).toHaveTextContent('Database billing is unavailable.');
  fail = false;
  mounted.rerender(<DatabaseBilling session={{ ...session, token: 'refreshed-token' }} />);
  if (action === 'checkout') await authorize();
  else fireEvent.click(await screen.findByRole('button', { name: 'Manage database billing' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('unsafe billing link');
  expect(screen.queryByRole('link')).not.toBeInTheDocument();
});

it('requires destructive confirmation, sends only the exact deletion confirmation, and refreshes deletion state', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let deleting = false;
  let deleted = false;
  const fetch = vi.fn(async (path: string, _options?: RequestInit) => {
    if (path.endsWith('/delete')) { deleting = true; return json({ deleting: true }); }
    return json({ ...plan, canCheckout: false, databaseState: deleted ? 'deleted' : deleting ? 'deleting' : 'retained', canDeleteDatabase: !deleting });
  });
  vi.stubGlobal('fetch', fetch);
  render(<DatabaseBilling session={session} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Delete organization database' }));
  expect(screen.getByText(/All organization records will be permanently removed/)).toHaveTextContent('Published dynamic pages will stop working. Already-incurred AWS charges remain payable.');
  expect(screen.getByText(/AWS may retain a system backup/)).toHaveTextContent('Table deletion halts further table costs. AWS may retain a system backup for up to 35 days after table deletion, without additional backup charges; deletion does not instantly physically erase that backup.');
  const button = screen.getByRole('button', { name: 'Permanently delete database' });
  expect(button).toBeDisabled();
  const input = screen.getByLabelText('Type DELETE DATABASE to confirm');
  fireEvent.change(input, { target: { value: 'delete database' } });
  expect(button).toBeDisabled();
  fireEvent.change(input, { target: { value: 'DELETE DATABASE' } });
  fireEvent.click(button);
  await screen.findByText(/Database: deletion in progress/);
  const request = fetch.mock.calls.find(([path]) => path.endsWith('/delete'))!;
  expect(request[1]?.method).toBe('POST');
  expect(JSON.parse(request[1]!.body as string)).toEqual({ confirmation: 'DELETE DATABASE' });
  expect(fetch.mock.calls.filter(([path]) => path === '/api/billing/database')).toHaveLength(2);
  expect(screen.queryByRole('button', { name: 'Delete organization database' })).not.toBeInTheDocument();
  deleted = true;
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  expect(await screen.findByText(/Database: deleted/)).toBeInTheDocument();
});

it('cancels deletion without a request and reports deletion service failures without claiming success', async () => {
  const fetch = vi.fn(async (path: string) => path.endsWith('/delete')
    ? json({ error: { message: 'Deletion unavailable.' } }, 503)
    : json({ ...plan, databaseState: 'retained', canDeleteDatabase: true }));
  vi.stubGlobal('fetch', fetch);
  render(<DatabaseBilling session={session} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Delete organization database' }));
  fireEvent.click(screen.getByRole('button', { name: 'Cancel deletion' }));
  expect(fetch).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole('button', { name: 'Delete organization database' }));
  expect(screen.getByLabelText('Type DELETE DATABASE to confirm')).toHaveValue('');
  fireEvent.change(screen.getByLabelText('Type DELETE DATABASE to confirm'), { target: { value: 'DELETE DATABASE' } });
  fireEvent.click(screen.getByRole('button', { name: 'Permanently delete database' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Deletion unavailable.');
  expect(screen.queryByText(/Database: deletion in progress/)).not.toBeInTheDocument();
});

it('rejects malformed deletion acknowledgements', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => path.endsWith('/delete') ? json({ deleting: 'true' })
    : json({ ...plan, databaseState: 'retained', canDeleteDatabase: true })));
  render(<DatabaseBilling session={session} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Delete organization database' }));
  fireEvent.change(screen.getByLabelText('Type DELETE DATABASE to confirm'), { target: { value: 'DELETE DATABASE' } });
  fireEvent.click(screen.getByRole('button', { name: 'Permanently delete database' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('invalid database deletion response');
});

it('does not expose stale links or consent after the signed-in customer changes', async () => {
  let resolveCheckout: (response: Response) => void = () => {};
  const fetch = vi.fn(async (path: string) => path.endsWith('/checkout')
    ? new Promise<Response>(resolve => { resolveCheckout = resolve; }) : json(plan));
  vi.stubGlobal('fetch', fetch);
  const mounted = render(<DatabaseBilling session={session} />);
  await authorize();
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  mounted.rerender(<DatabaseBilling session={{ token: 'other-token', actorId: 'other-customer' }} />);
  const button = await screen.findByRole('button', { name: 'Authorize database billing' });
  expect(button).toBeDisabled();
  expect(screen.getByRole('checkbox')).not.toBeChecked();
  await act(async () => { resolveCheckout(json({ url: 'https://checkout.stripe.com/c/setup/first-customer' })); });
  expect(screen.queryByRole('link')).not.toBeInTheDocument();
});

it('clears previously trusted invoices and actions when polling returns invalid costs', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let invalid = false;
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...plan, invoices: [{ ...invoice, chargeCents: invalid ? '2346' : 2346 }] })));
  render(<DatabaseBilling session={session} />);
  await screen.findByRole('link', { name: 'View invoice for 2026-10' });
  invalid = true;
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  expect(await screen.findByRole('alert')).toHaveTextContent('invalid database billing status or costs');
  expect(screen.queryByRole('link')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Authorize database billing' })).not.toBeInTheDocument();
});
