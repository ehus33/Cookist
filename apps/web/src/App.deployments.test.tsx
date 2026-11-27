import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from './App';
import * as apiModule from './api';

const session = { token: 'access-token', actorId: 'owner' };
const ownerAccess = { role: 'platform_owner', subscribed: true, paymentRequired: false, canList: true, canCreate: true, canDelete: true };

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it('explains missing membership without claiming Stripe checkout is unavailable', async () => {
  vi.spyOn(apiModule, 'api').mockResolvedValue({
    role: 'none', subscribed: false, paymentRequired: true,
    canList: false, canCreate: false, canDelete: false, canCheckout: false,
  });
  render(<App authenticatedSession={session} />);
  expect(await screen.findByText(/has not been assigned to an organization/)).toBeInTheDocument();
  expect(screen.queryByText(/Paid access is not available yet/)).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Subscribe with Stripe' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'New deployment' })).toBeDisabled();
});

it('opens only server-owned Stripe checkout and does not unlock on return', async () => {
  const admin = { token: 'admin-token', actorId: 'admin' };
  const access = { role: 'customer_admin', subscribed: false, paymentRequired: true,
    canList: true, canCreate: false, canDelete: true, canCheckout: true, canManageBilling: false };
  const api = vi.spyOn(apiModule, 'api').mockImplementation(async (path) => {
    if (path === '/api/deployment-access') return access;
    if (path === '/api/billing/checkout') return { url: 'https://checkout.stripe.com/c/pay/test' };
    return [];
  });
  window.history.replaceState(null, '', '/?billing=returned');
  try {
    render(<App authenticatedSession={admin} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Subscribe with Stripe' }));
    expect(await screen.findByRole('link', { name: 'Continue to secure Stripe checkout' })).toHaveAttribute('href', 'https://checkout.stripe.com/c/pay/test');
    expect(api).toHaveBeenCalledWith('/api/billing/checkout', admin, { method: 'POST' });
    expect(screen.getByRole('button', { name: 'New deployment' })).toBeDisabled();
    expect(screen.getByRole('heading', { name: 'Paid access required' })).toBeInTheDocument();
  } finally {
    window.history.replaceState(null, '', '/');
  }
});

it('opens the customer portal and rejects unexpected billing URLs', async () => {
  const access = { ...ownerAccess, role: 'customer_admin', canCheckout: false, canManageBilling: true };
  const api = vi.spyOn(apiModule, 'api').mockImplementation(async (path) => {
    if (path === '/api/deployment-access') return access;
    if (path === '/api/billing/portal') return { url: 'https://billing.stripe.com/p/session/test' };
    return [];
  });
  render(<App authenticatedSession={session} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Manage billing' }));
  expect(await screen.findByRole('link', { name: 'Continue to Stripe billing portal' })).toHaveAttribute('href', 'https://billing.stripe.com/p/session/test');
  api.mockImplementation(async (path) => {
    if (path === '/api/deployment-access') return access;
    if (path === '/api/billing/portal') return { url: 'https://billing.stripe.com.attacker.example/steal' };
    return [];
  });
  fireEvent.click(screen.getByRole('button', { name: 'Manage billing' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('invalid Stripe URL');
  expect(screen.queryByRole('link', { name: 'Continue to Stripe billing portal' })).not.toBeInTheDocument();
});

it('ignores late checkout results after switching accounts', async () => {
  const access = { ...ownerAccess, role: 'customer_admin', subscribed: false, paymentRequired: true,
    canCreate: false, canCheckout: true, canManageBilling: false };
  let finish!: (value: { url: string }) => void;
  vi.spyOn(apiModule, 'api').mockImplementation(async (path, currentSession) => {
    if (path === '/api/deployment-access') return currentSession?.actorId === 'owner' ? access : ownerAccess;
    if (path === '/api/billing/checkout') return new Promise<{ url: string }>((resolve) => { finish = resolve; });
    return [];
  });
  const { rerender } = render(<App authenticatedSession={session} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Subscribe with Stripe' }));
  rerender(<App authenticatedSession={{ token: 'other-token', actorId: 'other' }} />);
  await act(async () => { finish({ url: 'https://checkout.stripe.com/c/pay/old-account' }); });
  expect(screen.queryByRole('link', { name: 'Continue to secure Stripe checkout' })).not.toBeInTheDocument();
});

it('submits the selected business site and displays its deployment status', async () => {
  const api = vi.spyOn(apiModule, 'api').mockImplementation(async (_path, _session, options) => {
    if (_path === '/api/deployment-access') return ownerAccess;
    if (options?.method === 'POST') return { id: 'one', name: 'Shop', region: 'eu-west-1', status: 'running', style: 'warm' };
    return [];
  });
  render(<App authenticatedSession={session} />);
  await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  expect(screen.getByRole('radio', { name: /Blank slate/ })).toBeChecked();
  expect(screen.getByRole('radio', { name: /Classic/ })).toBeChecked();
  expect(screen.getByRole('combobox', { name: 'AWS region' })).toHaveValue('us-west-2');
  fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Shop' } });
  fireEvent.change(screen.getByLabelText('AWS region'), { target: { value: 'eu-west-1' } });
  fireEvent.click(screen.getByRole('radio', { name: /Business listing/ }));
  fireEvent.change(screen.getByLabelText('Business name'), { target: { value: 'My garage' } });
  fireEvent.change(screen.getByLabelText('Items, one per line'), { target: { value: 'Tire change' } });
  fireEvent.click(screen.getByRole('radio', { name: /Warm/ }));
  const preview = screen.getByRole('region', { name: 'Site preview' });
  expect(preview).toHaveClass('site-style-warm');
  expect(within(preview).getByText('My garage')).toBeInTheDocument();
  expect(within(preview).getByText('Tire change')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  await waitFor(() => expect(api).toHaveBeenCalledWith('/api/deployments', session, expect.objectContaining({
    method: 'POST',
    body: expect.objectContaining({ name: 'Shop', region: 'eu-west-1', template: 'business', style: 'warm', businessName: 'My garage', items: 'Tire change' }),
  })));
  expect(await screen.findByText('eu-west-1 — running')).toBeInTheDocument();
  expect(screen.getByText('Style: warm')).toBeInTheDocument();
  expect(screen.getByRole('status')).toHaveTextContent('Deployment started');
});

it('previews every style without switching templates or interpreting business text as HTML', () => {
  render(<App />);
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  expect(screen.getByText(/Your blank site stays empty/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole('radio', { name: /Business listing/ }));
  fireEvent.change(screen.getByLabelText('Business name'), { target: { value: '<script>example</script>' } });
  fireEvent.change(screen.getByLabelText('Items, one per line'), { target: { value: '  Oil & filter  \n\n<img src=x onerror=alert(1)>' } });
  const preview = screen.getByRole('region', { name: 'Site preview' });
  for (const [name, id] of [['Modern', 'modern'], ['Warm', 'warm'], ['Bold', 'bold'], ['Classic', 'classic']]) {
    fireEvent.click(screen.getByRole('radio', { name: new RegExp(name) }));
    expect(preview).toHaveClass(`site-style-${id}`);
    expect(screen.getByRole('radio', { name: /Business listing/ })).toBeChecked();
    expect(within(preview).getByText('<script>example</script>')).toBeInTheDocument();
    expect(within(preview).getAllByRole('listitem')).toHaveLength(2);
    expect(within(preview).getByText('Oil & filter')).toBeInTheDocument();
    expect(preview.querySelector('script, img')).toBeNull();
  }
  expect(screen.getByRole('button', { name: 'Deploy site' })).toBeDisabled();
});

it('does not allow unauthenticated deployment', () => {
  render(<App />);
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  expect(screen.getByRole('button', { name: 'Deploy site' })).toBeDisabled();
  expect(screen.getByText(/require signing in with an authorized account/)).toBeInTheDocument();
});

it('shows server errors and returns focus when cancelled', async () => {
  vi.spyOn(apiModule, 'api').mockImplementation(async (_path, _session, options) => {
    if (_path === '/api/deployment-access') return ownerAccess;
    if (options?.method === 'POST') throw new Error('Worker unavailable');
    return [];
  });
  render(<App authenticatedSession={session} />);
  const newDeployment = screen.getByRole('button', { name: 'New deployment' });
  await waitFor(() => expect(newDeployment).toBeEnabled());
  fireEvent.click(newDeployment);
  fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Site' } });
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Worker unavailable');
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(newDeployment).toHaveFocus();
});

it('requires confirmation and starts deletion only for completed deployments', async () => {
  const api = vi.spyOn(apiModule, 'api').mockImplementation(async (_path, _session, options) => {
    if (_path === '/api/deployment-access') return ownerAccess;
    if (options?.method === 'DELETE') return { id: 'ready', name: 'Shop', region: 'us-west-2', status: 'deleting' };
    return [
      { id: 'ready', name: 'Shop', region: 'us-west-2', status: 'ready' },
      { id: 'failed', name: 'Rocket', region: 'us-west-2', status: 'failed' },
      { id: 'running', name: 'Busy', region: 'us-west-2', status: 'running' },
    ];
  });
  render(<App authenticatedSession={session} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Delete Shop' }));
  expect(screen.getByRole('button', { name: 'Delete Rocket' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Delete Busy' })).not.toBeInTheDocument();
  expect(api).not.toHaveBeenCalledWith('/api/deployments/ready', session, { method: 'DELETE' });
  fireEvent.click(screen.getByRole('button', { name: 'Cancel deletion' }));
  expect(screen.queryByRole('button', { name: 'Confirm delete' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Delete Shop' }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
  await waitFor(() => expect(api).toHaveBeenCalledWith('/api/deployments/ready', session, { method: 'DELETE' }));
  expect(await screen.findByText('us-west-2 — deleting')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Delete Shop' })).not.toBeInTheDocument();
  expect(screen.getByRole('status')).toHaveTextContent('Resource deletion started');
});

it('shows cleanup errors without removing the deployment and allows retry confirmation', async () => {
  vi.spyOn(apiModule, 'api').mockImplementation(async (_path, _session, options) => {
    if (_path === '/api/deployment-access') return ownerAccess;
    if (options?.method === 'DELETE') throw new Error('Worker busy');
    return [{ id: 'failed', name: 'Rocket', region: 'us-west-2', status: 'cleanup_failed' }];
  });
  render(<App authenticatedSession={session} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Retry cleanup for Rocket' }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not confirm cleanup: Worker busy');
  expect(screen.getByText('us-west-2 — cleanup_failed')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Confirm delete' })).toBeEnabled();
});

it('gates unconfigured accounts without requesting deployment data', async () => {
    const api = vi.spyOn(apiModule, 'api').mockResolvedValue({
      role: 'none', subscribed: false, paymentRequired: true, canList: false, canCreate: false, canDelete: false,
    });
    render(<App authenticatedSession={session} />);
    expect(await screen.findByRole('heading', { name: 'Paid access required' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New deployment' })).toBeDisabled();
    expect(api).not.toHaveBeenCalledWith('/api/deployments', expect.anything(), expect.anything());
  });

it('allows expired deployers to clean up but not create and keeps viewers read-only', async () => {
    const api = vi.spyOn(apiModule, 'api').mockImplementation(async path => {
      if (path === '/api/deployment-access') return {
        role: 'deployer', subscribed: false, paymentRequired: true, canList: true, canCreate: false, canDelete: true,
      };
      return [{ id: 'site', name: 'Site', region: 'us-west-2', status: 'ready', url: 'https://example.cloudfront.net' }];
    });
    const { rerender } = render(<App authenticatedSession={session} />);
    expect(await screen.findByRole('button', { name: 'Delete Site' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New deployment' })).toBeDisabled();
    expect(screen.getByText(/Ask your organization administrator/)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Open site' })).not.toBeInTheDocument();
    api.mockImplementation(async path => {
      if (path === '/api/deployment-access') return {
        role: 'viewer', subscribed: true, paymentRequired: false, canList: true, canCreate: false, canDelete: false,
      };
      return [{ id: 'site', name: 'Site', region: 'us-west-2', status: 'ready' }];
    });
    rerender(<App authenticatedSession={{ token: 'viewer-token', actorId: 'viewer' }} />);
    expect(await screen.findByText(/read-only deployment access/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete Site' })).not.toBeInTheDocument();
  });

it('ignores an old account deployment response after switching accounts', async () => {
  let completeDeployment: ((value: unknown) => void) | undefined;
  const pending = new Promise<unknown>(resolve => { completeDeployment = resolve; });
  vi.spyOn(apiModule, 'api').mockImplementation(async (path, account, options) => {
    if (options?.method === 'POST') return pending;
    if (path === '/api/deployment-access') return account?.actorId === 'owner' ? ownerAccess
      : { role: 'viewer', subscribed: true, paymentRequired: false, canList: true, canCreate: false, canDelete: false };
    return [];
  });
  const { rerender } = render(<App authenticatedSession={session} />);
  await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Private site' } });
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  rerender(<App authenticatedSession={{ actorId: 'viewer', token: 'viewer-token' }} />);
  await screen.findByText(/read-only deployment access/);
  await act(async () => {
    if (!completeDeployment) throw new Error('Deployment request was not started.');
    completeDeployment({ id: 'private', name: 'Private site', region: 'us-west-2', status: 'running' });
    await pending;
  });
  expect(screen.queryByText('Private site')).not.toBeInTheDocument();
  expect(screen.queryByText(/Deployment started/)).not.toBeInTheDocument();
});

it('uses server-authorized local access without a payment notice', async () => {
  vi.spyOn(apiModule, 'api').mockImplementation(async path => {
    if (path === '/api/deployment-access') return {
      role: 'deployer', subscribed: false, paymentRequired: false, canList: true, canCreate: true, canDelete: true,
    };
    return [];
  });
  render(<App authenticatedSession={session} />);
  await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
  expect(screen.queryByRole('heading', { name: 'Paid access required' })).not.toBeInTheDocument();
});

  it('previews and submits seller products with exact integer USD prices', async () => {
    const api = vi.spyOn(apiModule, 'api').mockImplementation(async (path, _session, options) => {
      if (path === '/api/deployment-access') return ownerAccess;
      if (options?.method === 'POST') return { id: 'store', name: 'Shop', region: 'us-west-2', status: 'running', style: 'bold' };
      return [];
    });
    render(<App authenticatedSession={session} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
    fireEvent.click(screen.getByRole('radio', { name: /Storefront/ }));
    fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Shop' } });
    fireEvent.change(screen.getByLabelText('Store name'), { target: { value: 'My store' } });
    fireEvent.change(screen.getByLabelText('Product 1 name'), { target: { value: '<img src=x>' } });
    fireEvent.change(screen.getByLabelText('Product 1 description'), { target: { value: 'Oil & filter' } });
    fireEvent.change(screen.getByLabelText('Product 1 price (USD)'), { target: { value: '27.05' } });
    fireEvent.change(screen.getByLabelText('Product 1 Stripe Payment Link'), { target: { value: 'https://buy.stripe.com/test_abc123' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add product' }));
    fireEvent.change(screen.getByLabelText('Product 2 name'), { target: { value: 'Service' } });
    fireEvent.change(screen.getByLabelText('Product 2 price (USD)'), { target: { value: '0.01' } });
    fireEvent.change(screen.getByLabelText('Product 2 Stripe Payment Link'), { target: { value: 'https://buy.stripe.com/abc456' } });
    fireEvent.click(screen.getByRole('radio', { name: /Bold/ }));
    const preview = screen.getByRole('region', { name: 'Site preview' });
    expect(preview).toHaveClass('site-style-bold');
    expect(within(preview).getByText('$27.05 USD')).toBeInTheDocument();
    expect(within(preview).getByText('$0.01 USD')).toBeInTheDocument();
    expect(within(preview).getByText('<img src=x>')).toBeInTheDocument();
    expect(preview.querySelector('img, a')).toBeNull();
    for (const button of within(preview).getAllByRole('button', { name: 'Buy now' })) expect(button).toBeDisabled();
    expect(within(preview).getAllByText('Test checkout - no real charge')).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
    await waitFor(() => expect(api).toHaveBeenCalledWith('/api/deployments', session, expect.objectContaining({
      method: 'POST', body: expect.objectContaining({
        template: 'storefront', style: 'bold', businessName: 'My store',
        products: [
          { name: '<img src=x>', description: 'Oil & filter', priceCents: 2705, paymentLink: 'https://buy.stripe.com/test_abc123' },
          { name: 'Service', description: '', priceCents: 1, paymentLink: 'https://buy.stripe.com/abc456' },
        ],
      }),
    })));
  });

  it('rejects invalid storefront prices and checkout destinations before sending a request', async () => {
    const api = vi.spyOn(apiModule, 'api').mockImplementation(async path => path === '/api/deployment-access' ? ownerAccess : []);
    render(<App authenticatedSession={session} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
    fireEvent.click(screen.getByRole('radio', { name: /Storefront/ }));
    fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Shop' } });
    fireEvent.change(screen.getByLabelText('Store name'), { target: { value: 'Shop' } });
    fireEvent.change(screen.getByLabelText('Product 1 name'), { target: { value: 'Product' } });
    for (const [price, link] of [
      ['0', 'https://buy.stripe.com/abc123'], ['19.999', 'https://buy.stripe.com/abc123'],
      ['1e2', 'https://buy.stripe.com/abc123'], ['19.99', 'https://buy.stripe.com.attacker.example/abc123'],
      ['19.99', 'https://checkout.stripe.com/c/pay/platformBilling'],
    ]) {
      fireEvent.change(screen.getByLabelText('Product 1 price (USD)'), { target: { value: price } });
      fireEvent.change(screen.getByLabelText('Product 1 Stripe Payment Link'), { target: { value: link } });
      fireEvent.submit(screen.getByRole('button', { name: 'Deploy site' }).closest('form')!);
      expect(await screen.findByRole('alert')).toHaveTextContent('Product 1 needs');
    }
    expect(api.mock.calls.some(([, , options]) => options?.method === 'POST')).toBe(false);
  });

  it('removes product drafts and does not send storefront products with a blank template', async () => {
    const api = vi.spyOn(apiModule, 'api').mockImplementation(async (path, _session, options) => {
      if (path === '/api/deployment-access') return ownerAccess;
      if (options?.method === 'POST') return { id: 'blank', name: 'Site', region: 'us-west-2', status: 'running' };
      return [];
    });
    render(<App authenticatedSession={session} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
    fireEvent.click(screen.getByRole('radio', { name: /Storefront/ }));
    expect(screen.getByRole('button', { name: 'Remove product 1' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Add product' }));
    fireEvent.change(screen.getByLabelText('Product 2 name'), { target: { value: 'Removed' } });
    fireEvent.click(screen.getByRole('button', { name: 'Remove product 2' }));
    expect(within(screen.getByRole('region', { name: 'Site preview' })).queryByText('Removed')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('radio', { name: /Blank slate/ }));
    fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Site' } });
    fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
    await waitFor(() => expect(api).toHaveBeenCalledWith('/api/deployments', session, expect.objectContaining({
      method: 'POST', body: expect.not.objectContaining({ products: expect.anything() }),
    })));
  });
