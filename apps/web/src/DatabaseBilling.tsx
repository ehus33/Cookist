import { useEffect, useRef, useState } from 'react';
import { api, errorMessage } from './api';
import type { Session } from './types';

type Invoice = { period: string; awsCostUsd: string; chargeCents: number; status: string; url?: string };
type DatabaseStatus = {
  configured: boolean; serviceReady: boolean; subscribed: boolean; hasSubscription: boolean;
  canCheckout: boolean; canManageBilling: boolean; mode: 'test' | 'live';
  billingModel: 'aws-cost-markup' | 'fixed-monthly'; multiplier: '1.9'; currency: 'usd';
  databaseState: 'not_created' | 'retained' | 'deleting' | 'deleted'; canDeleteDatabase: boolean;
  invoices: Invoice[];
  notice?: string;
  price?: { amountCents: number; currency: 'usd'; interval: 'month' };
};

function trustedStripeUrl(value: string, kind: 'invoice' | 'portal' | 'checkout'): string {
  let url: URL;
  try { url = new URL(value); } catch { throw new Error('Stripe returned an unsafe billing link.'); }
  const allowed = kind === 'invoice'
    ? url.hostname === 'invoice.stripe.com' || url.hostname === 'pay.stripe.com'
    : url.hostname === (kind === 'portal' ? 'billing.stripe.com' : 'checkout.stripe.com');
  if (url.protocol !== 'https:' || !allowed || url.username || url.password || url.port
    || /^https:\/\/[^/?#]*:/i.test(value)
    || value !== value.trim() || /[\u0000-\u0020\\]/.test(value)) {
    throw new Error('Stripe returned an unsafe billing link.');
  }
  return url.href;
}

function isDatabaseStatus(value: unknown): value is DatabaseStatus {
  if (!value || typeof value !== 'object') return false;
  const status = value as Record<string, unknown>;
  if (!['configured', 'serviceReady', 'subscribed', 'hasSubscription', 'canCheckout', 'canManageBilling', 'canDeleteDatabase']
    .every(key => typeof status[key] === 'boolean') || (status.mode !== 'test' && status.mode !== 'live')
    || typeof status.billingModel !== 'string' || !['aws-cost-markup', 'fixed-monthly'].includes(status.billingModel)
    || status.multiplier !== '1.9' || status.currency !== 'usd'
    || typeof status.databaseState !== 'string' || !['not_created', 'retained', 'deleting', 'deleted'].includes(status.databaseState)
    || !Array.isArray(status.invoices) || (status.notice !== undefined && typeof status.notice !== 'string')) return false;
  if (status.canCheckout && (!status.configured || !status.serviceReady || status.hasSubscription || status.subscribed
    || status.databaseState === 'deleting')) return false;
  if (status.canDeleteDatabase && status.databaseState !== 'retained') return false;
  if (status.price !== undefined) {
    if (!status.price || typeof status.price !== 'object') return false;
    const price = status.price as Record<string, unknown>;
    if (!Number.isSafeInteger(price.amountCents) || (price.amountCents as number) <= 0
      || price.currency !== 'usd' || price.interval !== 'month') return false;
  }
  if (status.billingModel === 'fixed-monthly' && status.configured && !status.price) return false;
  return status.invoices.every((invoice: unknown) => {
    if (!invoice || typeof invoice !== 'object') return false;
    const item = invoice as Record<string, unknown>;
    if (typeof item.period !== 'string' || !/^\d{4}-(0[1-9]|1[0-2])$/.test(item.period)
      || typeof item.awsCostUsd !== 'string' || !/^-?(0|[1-9]\d*)(\.\d+)?$/.test(item.awsCostUsd)
      || !Number.isFinite(Number(item.awsCostUsd)) || Math.abs(Number(item.awsCostUsd)) > Number.MAX_SAFE_INTEGER / 100
      || !Number.isSafeInteger(item.chargeCents)
      || typeof item.status !== 'string' || !/^[a-z][a-z0-9_]{0,63}$/.test(item.status)) return false;
    if (item.url !== undefined) {
      if (typeof item.url !== 'string') return false;
      trustedStripeUrl(item.url, 'invoice');
    }
    return true;
  });
}

const money = (cents: number) => new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(cents / 100);
const invoiceStatuses: Record<string, string> = {
  draft: 'Draft', open: 'Open', paid: 'Paid', void: 'Void', uncollectible: 'Uncollectible',
  credited: 'Credit posted to Stripe customer balance (not a cash refund)',
  no_charge: 'Verified AWS cost rounds to zero — no Stripe charge',
};

export function DatabaseBilling({ session }: { session: Session }) {
  const [status, setStatus] = useState<DatabaseStatus | null>(null);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);
  const [authorized, setAuthorized] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [confirmation, setConfirmation] = useState('');
  const [link, setLink] = useState<{ url: string; portal: boolean } | null>(null);
  const identity = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const busy = useRef(false);
  const readSequence = useRef(0);

  const load = async (current: number, signal: AbortSignal) => {
    const request = ++readSequence.current;
    try {
      const value = await api<unknown>('/api/billing/database', session, { signal });
      if (!isDatabaseStatus(value)) throw new Error('The service returned invalid database billing status or costs. Please retry.');
      if (current !== identity.current || request !== readSequence.current || signal.aborted) return;
      setStatus(value); setError('');
      setLink(previous => previous && (previous.portal ? value.canManageBilling : value.canCheckout) ? previous : null);
      if (!value.canCheckout) setAuthorized(false);
      if (!value.canDeleteDatabase) { setConfirmingDelete(false); setConfirmation(''); }
    } catch (cause) {
      if (signal.aborted || current !== identity.current || request !== readSequence.current) return;
      setStatus(null); setLink(null); setAuthorized(false); setConfirmingDelete(false); setConfirmation('');
      setError(errorMessage(cause));
    }
  };

  useEffect(() => {
    const current = ++identity.current;
    const abort = new AbortController();
    controller.current = abort;
    busy.current = false;
    setStatus(null); setError(''); setLink(null); setPending(false);
    setAuthorized(false); setConfirmingDelete(false); setConfirmation('');
    void load(current, abort.signal);
    const timer = window.setInterval(() => { if (!busy.current) void load(current, abort.signal); }, 15000);
    return () => { ++identity.current; abort.abort(); window.clearInterval(timer); };
  }, [session]);

  const start = async (action: 'checkout' | 'portal' | 'delete') => {
    const signal = controller.current?.signal;
    if (busy.current || !status || !signal || signal.aborted
      || (action === 'portal' ? !status.canManageBilling : action === 'delete'
        ? !status.canDeleteDatabase || !confirmingDelete || confirmation !== 'DELETE DATABASE'
        : !status.canCheckout || (status.billingModel === 'aws-cost-markup' && !authorized))) return;
    const current = identity.current;
    ++readSequence.current;
    busy.current = true;
    setPending(true); setError(''); setLink(null);
    try {
      const result = await api<unknown>(action === 'portal' ? '/api/billing/portal' : `/api/billing/database/${action}`, session, {
        method: 'POST', signal, ...(action === 'delete' ? { body: { confirmation: 'DELETE DATABASE' } } : {}),
      });
      if (current !== identity.current || signal.aborted) return;
      if (action === 'delete') {
        if (!result || typeof result !== 'object' || !('deleting' in result) || result.deleting !== true) {
          throw new Error('The service returned an invalid database deletion response.');
        }
        setStatus(previous => previous ? { ...previous, databaseState: 'deleting', canDeleteDatabase: false, canCheckout: false } : null);
        setConfirmingDelete(false); setConfirmation(''); setAuthorized(false);
        await load(current, signal);
      } else {
        if (!result || typeof result !== 'object' || !('url' in result) || typeof result.url !== 'string') {
          throw new Error('Stripe did not return a billing link.');
        }
        const url = trustedStripeUrl(result.url, action);
        setLink({ url, portal: action === 'portal' });
        if (action === 'checkout') setAuthorized(false);
      }
    } catch (cause) {
      if (current === identity.current && !signal.aborted) setError(errorMessage(cause));
    } finally {
      if (current === identity.current) { busy.current = false; setPending(false); }
    }
  };

  return <section className="database-billing" aria-label="Database add-on">
    <h3>App database billing</h3>
    <p>Your organization has a separate database. AWS bills Cookist; you do not need an AWS account. Database billing is independent of your Cookist plan.</p>
    {status?.billingModel === 'aws-cost-markup' && <>
      <p>The variable charge is the prior calendar month's actual AWS-tagged DynamoDB cost for your organization × 1.9, rounded to cents in USD and invoiced monthly in arrears. Other AWS services and unattributed charges are excluded. Estimates are not actual costs.</p>
      <p>Retained storage continues to incur billable AWS costs until you explicitly delete the database, even if you cancel billing or your Cookist plan.</p>
    </>}
    {status?.billingModel === 'fixed-monthly' && status.price && <p><strong>{money(status.price.amountCents)} / month per organization</strong>, in addition to your Cookist plan. This legacy subscription is a fixed monthly fee, not actual AWS cost billing.</p>}
    {status && <p className="deployment-notice">
      {!status.configured ? 'Database billing is not configured.' : !status.serviceReady ? 'The operator must enable the app database service before billing authorization.' :
        status.subscribed ? 'Database billing active.' : status.hasSubscription ? 'Database billing authorization is pending or inactive. Manage existing billing; do not start another setup.' :
          status.canCheckout ? 'Authorize database billing to enable connected member apps after server verification.' : 'An active Cookist plan and database billing authorization are required to publish connected member apps.'}
      {status.mode === 'test' && ' Stripe test mode: no live charges.'}
    </p>}
    {status?.notice && <p role="alert">{status.notice}</p>}
    {status?.canCheckout && <>
      {status.billingModel === 'aws-cost-markup' && <label>
        <input type="checkbox" checked={authorized} disabled={pending} onChange={event => setAuthorized(event.target.checked)} />
        I authorize variable monthly charges for my organization's actual AWS-tagged DynamoDB cost × 1.9, rounded to cents, invoiced in arrears, including retained storage until explicit database deletion.
      </label>}
      <button type="button" disabled={pending || (status.billingModel === 'aws-cost-markup' && !authorized)} onClick={() => void start('checkout')}>
        {status.billingModel === 'aws-cost-markup' ? 'Authorize database billing' : 'Add database subscription'}
      </button>
    </>}
    {status?.canManageBilling && <button type="button" disabled={pending} onClick={() => void start('portal')}>Manage database billing</button>}
    {pending && <p role="status">Processing database billing request...</p>}
    {!status && !error && <p role="status">Loading database billing...</p>}
    {link && <p><a href={link.url} rel="noreferrer">{link.portal ? 'Continue to database billing portal' : 'Continue to database checkout'}</a>
      {!link.portal && (status?.billingModel === 'aws-cost-markup'
        ? ' Stripe setup saves your payment method; returning here does not confirm payment or authorize access. Only verified server status enables database access.'
        : ' Access unlocks only after verified server status.')}</p>}
    <p>Manage your payment method or cancel database billing in Stripe. Cancelling your Cookist plan does not cancel database billing, and cancelling billing does not delete retained records.</p>
    {status && <p role="status">Database: {({ not_created: 'not created', retained: 'retained — storage remains billable until deletion', deleting: 'deletion in progress', deleted: 'deleted' })[status.databaseState]}.</p>}
    {status?.canDeleteDatabase && !confirmingDelete && <button type="button" disabled={pending} onClick={() => setConfirmingDelete(true)}>Delete organization database</button>}
    {status?.canDeleteDatabase && confirmingDelete && <div role="group" aria-label="Confirm permanent database deletion">
      <p>All organization records will be permanently removed from the managed database. Published dynamic pages will stop working. Already-incurred AWS charges remain payable. Table deletion halts further table costs. AWS may retain a system backup for up to 35 days after table deletion, without additional backup charges; deletion does not instantly physically erase that backup. This cannot be undone.</p>
      <label>Type DELETE DATABASE to confirm
        <input value={confirmation} disabled={pending} onChange={event => setConfirmation(event.target.value)} />
      </label>
      <button type="button" disabled={pending || confirmation !== 'DELETE DATABASE'} onClick={() => void start('delete')}>Permanently delete database</button>
      <button type="button" disabled={pending} onClick={() => { setConfirmingDelete(false); setConfirmation(''); }}>Cancel deletion</button>
    </div>}
    {!!status?.invoices.length && <>
      <h4>Database invoices</h4>
      <table><caption>Monthly actual costs and cumulative billed charges. Negative charges are AWS credits applied to your future Stripe balance. Unpaid overstated invoices are voided and replaced; reductions to paid amounts become future Stripe customer-balance credits, not cash refunds.</caption>
        <thead><tr><th scope="col">Calendar month</th><th scope="col">Actual AWS DynamoDB cost (USD)</th><th scope="col">Cumulative charge (USD)</th><th scope="col">Status</th><th scope="col">Invoice</th></tr></thead>
        <tbody>{status.invoices.map((invoice, index) => <tr key={`${invoice.period}-${index}`}>
          <td>{invoice.period}</td><td>{invoice.awsCostUsd.startsWith('-') ? `-$${invoice.awsCostUsd.slice(1)}` : `$${invoice.awsCostUsd}`}</td><td>{money(invoice.chargeCents)}</td>
          <td>{Object.hasOwn(invoiceStatuses, invoice.status) ? invoiceStatuses[invoice.status] : 'Unknown invoice status'}</td>
          <td>{invoice.url && <a href={invoice.url} rel="noreferrer">View invoice for {invoice.period}</a>}</td>
        </tr>)}</tbody>
      </table>
    </>}
    {error && <p role="alert">{error}</p>}
  </section>;
}
