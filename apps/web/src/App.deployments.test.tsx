import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from './App';
import * as apiModule from './api';

const session = { token: 'access-token', actorId: 'owner' };

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it('submits the selected business site and displays its deployment status', async () => {
  const api = vi.spyOn(apiModule, 'api').mockImplementation(async (_path, _session, options) => {
    if (options?.method === 'POST') return { id: 'one', name: 'Shop', region: 'eu-west-1', status: 'running' };
    return [];
  });
  render(<App authenticatedSession={session} />);
  await waitFor(() => expect(api).toHaveBeenCalled());
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  expect(screen.getByRole('radio', { name: /Blank slate/ })).toBeChecked();
  expect(screen.getByRole('combobox', { name: 'AWS region' })).toHaveValue('us-west-2');
  fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Shop' } });
  fireEvent.change(screen.getByLabelText('AWS region'), { target: { value: 'eu-west-1' } });
  fireEvent.click(screen.getByRole('radio', { name: /Business listing/ }));
  fireEvent.change(screen.getByLabelText('Business name'), { target: { value: 'My garage' } });
  fireEvent.change(screen.getByLabelText('Items, one per line'), { target: { value: 'Tire change' } });
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  await waitFor(() => expect(api).toHaveBeenCalledWith('/api/deployments', session, expect.objectContaining({
    method: 'POST',
    body: expect.objectContaining({ name: 'Shop', region: 'eu-west-1', template: 'business', businessName: 'My garage', items: 'Tire change' }),
  })));
  expect(await screen.findByText('eu-west-1 — running')).toBeInTheDocument();
  expect(screen.getByRole('status')).toHaveTextContent('Deployment started');
});

it('does not allow unauthenticated deployment', () => {
  render(<App />);
  fireEvent.click(screen.getByRole('button', { name: 'New deployment' }));
  expect(screen.getByRole('button', { name: 'Deploy site' })).toBeDisabled();
  expect(screen.getByText(/require signing in as the configured operator/)).toBeInTheDocument();
});

it('shows server errors and returns focus when cancelled', async () => {
  vi.spyOn(apiModule, 'api').mockImplementation(async (_path, _session, options) => {
    if (options?.method === 'POST') throw new Error('Worker unavailable');
    return [];
  });
  render(<App authenticatedSession={session} />);
  const newDeployment = screen.getByRole('button', { name: 'New deployment' });
  fireEvent.click(newDeployment);
  fireEvent.change(screen.getByLabelText('Deployment name'), { target: { value: 'Site' } });
  fireEvent.click(screen.getByRole('button', { name: 'Deploy site' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Worker unavailable');
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(newDeployment).toHaveFocus();
});
