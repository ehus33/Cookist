import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { DynamicAdmin } from './DynamicAdmin';
import { defaultDynamicCollection } from './dynamicApps';

const session = { token: 'owner-token', actorId: 'owner' };
const deployment = { id: 'site', name: 'Portal' };
const collection = { ...defaultDynamicCollection(), fields: [
  ...defaultDynamicCollection().fields,
  { id: 'amount', label: 'Amount', type: 'number' as const, required: false },
  { id: 'approved', label: 'Approved', type: 'boolean' as const, required: true },
  { id: 'email', label: 'Email', type: 'email' as const, required: false },
] };
const design = { dynamic: { collections: [collection] } };
const record = { id: 'record-1', values: { title: 'First', amount: 1.5, approved: false }, version: 1,
  createdAt: '2026-11-27T10:00:00Z', updatedAt: '2026-11-27T10:00:00Z' };
function json(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }); }
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it('loads raw design, creates typed records, edits with versions and confirms DELETE with If-Match', async () => {
  const fetch = vi.fn(async (_path: string, options?: RequestInit) => {
    if (_path.endsWith('/design')) return json(design);
    if (options?.method === 'POST') return json({ record: { ...record, values: JSON.parse(options.body as string).values } });
    if (options?.method === 'PATCH') return json({ record: { ...record, values: JSON.parse(options.body as string).values, version: 2 } });
    if (options?.method === 'DELETE') return json({ deleted: true });
    return json({ records: [] });
  });
  vi.stubGlobal('fetch', fetch);
  render(<DynamicAdmin deployment={deployment} session={session} onClose={() => {}} />);
  await waitFor(() => expect(screen.getByRole('button', { name: 'New record' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'New record' }));
  fireEvent.click(screen.getByRole('button', { name: 'Save record' }));
  expect(fetch.mock.calls.some(([, options]) => options?.method === 'POST')).toBe(false);
  fireEvent.change(screen.getByLabelText('Title (required)'), { target: { value: '<b>New</b>' } });
  fireEvent.change(screen.getByLabelText('Amount'), { target: { value: '3.25' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save record' }));
  await screen.findByRole('heading', { name: 'Record record-1' });
  const create = fetch.mock.calls.find(([, options]) => options?.method === 'POST')!;
  expect(JSON.parse(create[1]!.body as string)).toEqual({ values: { title: '<b>New</b>', amount: 3.25, approved: false } });
  expect(screen.getByText('<b>New</b>')).toBeInTheDocument();
  expect(screen.getByText('<b>New</b>').querySelector('b')).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Edit record' }));
  fireEvent.change(screen.getByLabelText('Title (required)'), { target: { value: 'Updated' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save record' }));
  await screen.findByText(/Version: 2/);
  const patch = fetch.mock.calls.find(([, options]) => options?.method === 'PATCH')!;
  expect(JSON.parse(patch[1]!.body as string)).toEqual({ values: { title: 'Updated', amount: 3.25, approved: false }, version: 1 });
  fireEvent.click(screen.getByRole('button', { name: 'Delete record' }));
  expect(fetch.mock.calls.some(([, options]) => options?.method === 'DELETE')).toBe(false);
  fireEvent.click(screen.getByRole('button', { name: 'Cancel deletion' }));
  fireEvent.click(screen.getByRole('button', { name: 'Delete record' }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
  await waitFor(() => expect(screen.queryByLabelText('Record details')).not.toBeInTheDocument());
  const remove = fetch.mock.calls.find(([, options]) => options?.method === 'DELETE')!;
  expect(remove[1]!.headers).toEqual(expect.objectContaining({ 'If-Match': '2', Authorization: 'Bearer owner-token' }));
});

it('paginates using encoded cursors and exposes list and mutation errors without removing data', async () => {
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path.endsWith('/design')) return json(design);
    if (options?.method === 'DELETE') return json({ error: { message: 'Record changed. Refresh first.' } }, 409);
    if (path.includes('?cursor=')) return json({ records: [{ ...record, id: 'record-2', values: { title: 'Second' } }] });
    return json({ records: [record], next: 'a+/=' });
  });
  vi.stubGlobal('fetch', fetch);
  render(<DynamicAdmin deployment={deployment} session={session} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Load more records' }));
  await screen.findByRole('button', { name: 'Second (record-2)' });
  expect(fetch).toHaveBeenCalledWith('/api/deployments/site/collections/requests/records?cursor=a%2B%2F%3D', expect.anything());
  fireEvent.click(screen.getByRole('button', { name: 'First (record-1)' }));
  fireEvent.click(screen.getByRole('button', { name: 'Delete record' }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Record changed');
  expect(screen.getByRole('heading', { name: 'Record record-1' })).toBeInTheDocument();
});

it('refreshes selected records with current versions and reports record-list failures', async () => {
  let reads = 0;
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    if (path.endsWith('/design')) return json(design);
    reads += 1;
    if (reads === 3) return json({ error: { message: 'Data unavailable.' } }, 503);
    return json({ records: [{ ...record, version: reads }] });
  }));
  render(<DynamicAdmin deployment={deployment} session={session} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: 'First (record-1)' }));
  fireEvent.click(screen.getByRole('button', { name: 'Refresh records' }));
  await screen.findByText(/Version: 2/);
  fireEvent.click(screen.getByRole('button', { name: 'Refresh records' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Data unavailable');
  expect(screen.getByRole('heading', { name: 'Record record-1' })).toBeInTheDocument();
});

it('recognizes a disabled raw design and closes the manager', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ template: 'builder' })));
  const onClose = vi.fn();
  render(<DynamicAdmin deployment={deployment} session={session} onClose={onClose} />);
  await screen.findByText('Data is not enabled for this site.');
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Close data manager' }));
  expect(onClose).toHaveBeenCalledOnce();
});

it.each([
  ['design', { design }],
  ['records', { records: [{ ...record, values: { bad: { nested: true } } }] }],
  ['records', { records: [record], next: 42 }],
  ['POST', { record: { ...record, version: '1' } }],
  ['PATCH', { record: { ...record, id: 'wrong-record' } }],
  ['DELETE', { deleted: false }],
])('rejects malformed %s success responses', async (stage, bad) => {
  vi.stubGlobal('fetch', vi.fn(async (path: string, options?: RequestInit) => {
    if (path.endsWith('/design')) return json(stage === 'design' ? bad : design);
    if (options?.method === stage) return json(bad);
    return json(stage === 'records' ? bad : { records: [record] });
  }));
  render(<DynamicAdmin deployment={deployment} session={session} onClose={() => {}} />);
  if (stage === 'POST') {
    await waitFor(() => expect(screen.getByRole('button', { name: 'New record' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'New record' }));
    fireEvent.change(screen.getByLabelText('Title (required)'), { target: { value: 'New' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save record' }));
  } else if (stage === 'PATCH') {
    fireEvent.click(await screen.findByRole('button', { name: 'First (record-1)' }));
    fireEvent.click(screen.getByRole('button', { name: 'Edit record' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save record' }));
  } else if (stage === 'DELETE') {
    fireEvent.click(await screen.findByRole('button', { name: 'First (record-1)' }));
    fireEvent.click(screen.getByRole('button', { name: 'Delete record' }));
    fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
  }
  expect(await screen.findByRole('alert')).toHaveTextContent('malformed data');
});

it('resets deployment and session identity and ignores old in-flight reads', async () => {
  let resolveOld!: (response: Response) => void;
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path.endsWith('/design')) return json(design);
    if (path.includes('/site/') && (options?.headers as Record<string, string>).Authorization === 'Bearer owner-token')
      return new Promise<Response>(resolve => { resolveOld = resolve; });
    return json({ records: [{ ...record, id: 'fresh', values: { title: 'Fresh' } }] });
  });
  vi.stubGlobal('fetch', fetch);
  const { rerender } = render(<DynamicAdmin deployment={deployment} session={session} onClose={() => {}} />);
  await waitFor(() => expect(resolveOld).toBeTypeOf('function'));
  rerender(<DynamicAdmin deployment={{ id: 'other', name: 'Other' }} session={session} onClose={() => {}} />);
  expect(screen.queryByText('First (record-1)')).not.toBeInTheDocument();
  await screen.findByRole('button', { name: 'Fresh (fresh)' });
  resolveOld(json({ records: [record] }));
  await waitFor(() => expect(within(screen.getByRole('list', { name: 'Records' })).getAllByRole('button')).toHaveLength(1));
  fireEvent.click(screen.getByRole('button', { name: 'Fresh (fresh)' }));
  fireEvent.click(screen.getByRole('button', { name: 'Edit record' }));
  rerender(<DynamicAdmin deployment={{ id: 'other', name: 'Other' }} session={{ token: 'new-token', actorId: 'different' }} onClose={() => {}} />);
  expect(screen.queryByLabelText('Edit record')).not.toBeInTheDocument();
  await screen.findByRole('button', { name: 'Fresh (fresh)' });
  expect(fetch.mock.calls.some(([, options]) => (options?.headers as Record<string, string>).Authorization === 'Bearer new-token')).toBe(true);
});
