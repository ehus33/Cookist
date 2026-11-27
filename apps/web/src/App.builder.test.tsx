import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from './App';
import * as apiModule from './api';

const session = { token: 'test-token', actorId: 'owner' };
const ownerAccess = { role: 'platform_owner', subscribed: true, paymentRequired: false, canList: true, canCreate: true, canDelete: true };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it('sends the designed element order and settings through the existing authorized deployment API', async () => {
  const api = vi.spyOn(apiModule, 'api').mockImplementation(async (path, _session, options) => {
    if (path === '/api/deployment-access') return ownerAccess;
    if (options?.method === 'POST') return { id: 'one', name: 'My page', region: 'us-west-2', status: 'running' };
    return [];
  });
  render(<App authenticatedSession={session} />);
  await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  fireEvent.click(screen.getByRole('radio', { name: /Visual builder/ }));
  fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'My page' } });
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Add between 1 and 40 elements');
  fireEvent.click(screen.getByRole('button', { name: 'Add Heading' }));
  fireEvent.change(screen.getByLabelText('Element text'), { target: { value: 'Welcome home' } });
  fireEvent.change(screen.getByLabelText('Alignment'), { target: { value: 'center' } });
  fireEvent.click(screen.getByRole('button', { name: 'Add Button' }));
  fireEvent.change(screen.getByLabelText('Element text'), { target: { value: 'Visit us' } });
  fireEvent.change(screen.getByLabelText('Button URL'), { target: { value: 'javascript:alert(1)' } });
  fireEvent.submit(screen.getByRole('button', { name: 'Deploy site' }).closest('form')!);
  expect(await screen.findByRole('alert')).toHaveTextContent('Element 2 needs a complete HTTPS URL');
  fireEvent.change(screen.getByLabelText('Button URL'), { target: { value: 'https://example.com' } });
  fireEvent.click(screen.getByRole('button', { name: 'Place beside previous' }));
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  await waitFor(() => expect(api).toHaveBeenCalledWith('/api/deployments', session, expect.objectContaining({
    method: 'POST', body: expect.objectContaining({
      template: 'builder', blocks: [
        expect.objectContaining({ type: 'heading', text: 'Welcome home', align: 'center' }),
        expect.objectContaining({ type: 'button', text: 'Visit us', url: 'https://example.com', beside: true }),
      ],
    }),
  })));
});

it('reuses product configuration and excludes it when the catalog is removed', async () => {
  const api = vi.spyOn(apiModule, 'api').mockImplementation(async path => path === '/api/deployment-access' ? ownerAccess : []);
  render(<App authenticatedSession={session} />);
  await waitFor(() => expect(screen.getByRole('button', { name: 'New deployment' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  fireEvent.click(screen.getByRole('radio', { name: /Visual builder/ }));
  fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Page' } });
  fireEvent.click(screen.getByRole('button', { name: 'Add Product catalog' }));
  expect(screen.getByLabelText('Product 1 Stripe Payment Link')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Delete element' }));
  expect(screen.queryByLabelText('Product 1 Stripe Payment Link')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Add Text' }));
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  await waitFor(() => expect(api).toHaveBeenCalledWith('/api/deployments', session, expect.objectContaining({
    method: 'POST', body: expect.not.objectContaining({ products: expect.anything() }),
  })));
});
