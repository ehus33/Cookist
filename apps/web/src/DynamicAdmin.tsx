import { useEffect, useRef, useState } from 'react';
import { api, errorMessage } from './api';
import { isDynamicConfig, isObject, type DynamicCollection } from './dynamicApps';
import type { Session } from './types';

interface DataRecord {
  id: string;
  values: Record<string, string | number | boolean>;
  version: number;
  createdAt: string;
  updatedAt: string;
}
export interface DynamicAdminProps {
  deployment: { id: string; name: string };
  session: Session;
  onClose: () => void;
}

function isRecord(value: unknown): value is DataRecord {
  return isObject(value) && typeof value.id === 'string' && value.id.length > 0
    && isObject(value.values) && Object.values(value.values).every(item =>
      typeof item === 'string' || typeof item === 'boolean' || (typeof item === 'number' && Number.isFinite(item)))
    && typeof value.version === 'number' && Number.isSafeInteger(value.version) && value.version >= 1
    && typeof value.createdAt === 'string' && Number.isFinite(Date.parse(value.createdAt))
    && typeof value.updatedAt === 'string' && Number.isFinite(Date.parse(value.updatedAt));
}
const malformed = 'The service returned malformed data. Please try again.';

export function DynamicAdmin(props: DynamicAdminProps) {
  return <AdminContents key={JSON.stringify([props.deployment.id, props.session.actorId, props.session.token])} {...props} />;
}

function AdminContents({ deployment, session, onClose }: DynamicAdminProps) {
  const [collections, setCollections] = useState<DynamicCollection[]>([]);
  const [collectionId, setCollectionId] = useState('');
  const [records, setRecords] = useState<DataRecord[]>([]);
  const [next, setNext] = useState<string>();
  const [selected, setSelected] = useState<DataRecord>();
  const [editing, setEditing] = useState(false);
  const [values, setValues] = useState<Record<string, string | boolean>>({});
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);
  const controller = useRef<AbortController | null>(null);
  const inputs = useRef<HTMLDivElement>(null);
  const base = `/api/deployments/${encodeURIComponent(deployment.id)}`;
  const collection = collections.find(item => item.id === collectionId);
  const recordPath = `${base}/collections/${encodeURIComponent(collectionId)}/records`;

  useEffect(() => {
    const abort = new AbortController();
    setLoading(true);
    setError('');
    void api<unknown>(`${base}/design`, session, { signal: abort.signal }).then(result => {
      if (abort.signal.aborted) return;
      if (!isObject(result) || (result.dynamic === undefined
        ? typeof result.template !== 'string'
        : !isDynamicConfig(result.dynamic))) throw new Error(malformed);
      const config = result.dynamic;
      const available = isDynamicConfig(config) ? config.collections : [];
      setCollections(available);
      setCollectionId(available[0]?.id ?? '');
    }).catch(cause => {
      if (!abort.signal.aborted) setError(errorMessage(cause));
    }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [base, session.token, session.actorId, reload]);

  async function loadRecords(abort: AbortController, cursor?: string) {
    setLoading(true);
    setError('');
    try {
      const result = await api<unknown>(`${recordPath}${cursor === undefined ? '' : `?cursor=${encodeURIComponent(cursor)}`}`, session, { signal: abort.signal });
      if (abort.signal.aborted) return;
      if (!isObject(result) || !Array.isArray(result.records) || !result.records.every(isRecord)
        || (result.next !== undefined && (typeof result.next !== 'string' || !result.next.length))) throw new Error(malformed);
      const page = result.records;
      setRecords(previous => cursor === undefined ? page : [...previous, ...page.filter(item => !previous.some(old => old.id === item.id))]);
      if (cursor === undefined) {
        setSelected(previous => page.find(item => item.id === previous?.id));
        setEditing(false);
        setConfirmDelete(false);
      }
      setNext(result.next as string | undefined);
    } catch (cause) {
      if (!abort.signal.aborted) setError(errorMessage(cause));
    } finally { if (!abort.signal.aborted) setLoading(false); }
  }

  useEffect(() => {
    const abort = new AbortController();
    controller.current = abort;
    setRecords([]);
    setNext(undefined);
    setSelected(undefined);
    setEditing(false);
    setConfirmDelete(false);
    setValues({});
    setBusy(false);
    if (collectionId) void loadRecords(abort);
    return () => abort.abort();
    // Identity is reset by the keyed component; collection changes cancel reads and writes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [collectionId]);

  function edit(record?: DataRecord) {
    setSelected(record);
    setValues(Object.fromEntries(collection!.fields.map(field => [
      field.id, field.type === 'boolean' ? record?.values[field.id] === true : String(record?.values[field.id] ?? ''),
    ])));
    setEditing(true);
    setConfirmDelete(false);
    setError('');
  }
  async function save() {
    if (!collection || !controller.current || busy || loading) return;
    if (Array.from(inputs.current?.querySelectorAll('input') ?? []).some(input => !input.reportValidity())) return;
    const payload: DataRecord['values'] = {};
    for (const field of collection.fields) {
      const value = values[field.id];
      if (field.type === 'boolean') payload[field.id] = value === true;
      else {
        const text = typeof value === 'string' ? value : '';
        if (!text.trim()) {
          if (field.required) { setError(`${field.label} is required.`); return; }
        } else if (field.type === 'number') {
          if (!Number.isFinite(Number(text))) { setError(`${field.label} must be a finite number.`); return; }
          payload[field.id] = Number(text);
        } else payload[field.id] = text;
      }
    }
    const abort = controller.current;
    setBusy(true);
    setError('');
    try {
      const result = await api<unknown>(selected ? `${recordPath}/${encodeURIComponent(selected.id)}` : recordPath, session, {
        method: selected ? 'PATCH' : 'POST',
        body: selected ? { values: payload, version: selected.version } : { values: payload },
        signal: abort.signal,
      });
      if (abort.signal.aborted) return;
      if (!isObject(result) || !isRecord(result.record) || (selected && result.record.id !== selected.id)) throw new Error(malformed);
      const record = result.record;
      setRecords(previous => selected ? previous.map(item => item.id === record.id ? record : item) : [record, ...previous]);
      setSelected(record);
      setEditing(false);
    } catch (cause) { if (!abort.signal.aborted) setError(errorMessage(cause)); }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }
  async function remove() {
    if (!selected || !controller.current || busy || loading) return;
    const abort = controller.current;
    setBusy(true);
    setError('');
    try {
      const result = await api<unknown>(`${recordPath}/${encodeURIComponent(selected.id)}`, session, {
        method: 'DELETE', headers: { 'If-Match': String(selected.version) }, signal: abort.signal,
      });
      if (abort.signal.aborted) return;
      if (!isObject(result) || result.deleted !== true) throw new Error(malformed);
      setRecords(previous => previous.filter(item => item.id !== selected.id));
      setSelected(undefined);
      setConfirmDelete(false);
    } catch (cause) { if (!abort.signal.aborted) setError(errorMessage(cause)); }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }

  return <section className="dynamic-data-panel" aria-label={`Manage data for ${deployment.name}`}>
    <h2>Manage data: {deployment.name}</h2>
    <button type="button" onClick={onClose}>Close data manager</button>
    <p>You are managing records as the Cookist owner. Visitor member accounts are separate accounts on your site.</p>
    {error && <p role="alert">{error}</p>}
    {loading && <p role="status">Loading data…</p>}
    {busy && <p role="status">Saving changes…</p>}
    {!loading && !collections.length && !error && <p>Data is not enabled for this site.</p>}
    {!collections.length && error && <button type="button" disabled={loading} onClick={() => setReload(previous => previous + 1)}>Retry configuration</button>}
    {!!collections.length && <label>Collection<select value={collectionId} disabled={busy} onChange={event => {
      controller.current?.abort();
      setCollectionId(event.target.value);
    }}>{collections.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}
    {collection && <>
      <button type="button" disabled={loading || busy} onClick={() => edit()}>New record</button>
      <button type="button" disabled={loading || busy} onClick={() => controller.current && void loadRecords(controller.current)}>Refresh records</button>
      {!loading && !records.length && <p>No records.</p>}
      <ul aria-label="Records">{records.map(record => <li key={record.id}>
        <button type="button" disabled={busy} aria-pressed={selected?.id === record.id} onClick={() => {
          setSelected(record); setEditing(false); setConfirmDelete(false); setError('');
        }}>{String(record.values[collection.fields[0]?.id] ?? record.id)} ({record.id})</button>
      </li>)}</ul>
      {next && <button type="button" disabled={loading || busy} onClick={() => controller.current && void loadRecords(controller.current, next)}>Load more records</button>}
      {editing ? <div ref={inputs} aria-label={selected ? 'Edit record' : 'Create record'}>
        <fieldset disabled={busy || loading}>
          <legend>{selected ? 'Edit record' : 'Create record'}</legend>
          {collection.fields.map(field => <label key={field.id}>{field.label}{field.required ? ' (required)' : ''}
            {field.type === 'boolean'
              ? <input type="checkbox" checked={values[field.id] === true} onChange={event => setValues(previous => ({ ...previous, [field.id]: event.target.checked }))} />
              : <input type={field.type} step={field.type === 'number' ? 'any' : undefined} required={field.required} value={String(values[field.id] ?? '')}
                onChange={event => setValues(previous => ({ ...previous, [field.id]: event.target.value }))} />}
          </label>)}
          <button type="button" onClick={() => void save()}>Save record</button>
          <button type="button" onClick={() => { setEditing(false); setError(''); }}>Cancel editing</button>
        </fieldset>
      </div> : selected && <div aria-label="Record details">
        <h3>Record {selected.id}</h3>
        <dl>{collection.fields.map(field => <div key={field.id}><dt>{field.label}</dt><dd>{String(selected.values[field.id] ?? '—')}</dd></div>)}</dl>
        <p>Created: {selected.createdAt} · Updated: {selected.updatedAt} · Version: {selected.version}</p>
        <button type="button" disabled={busy || loading} onClick={() => edit(selected)}>Edit record</button>
        <button type="button" disabled={busy || loading} onClick={() => setConfirmDelete(true)}>Delete record</button>
        {confirmDelete && <div role="group" aria-label="Confirm deletion">
          <p>Delete this record permanently?</p>
          <button type="button" disabled={busy || loading} onClick={() => void remove()}>Confirm delete</button>
          <button type="button" disabled={busy} onClick={() => setConfirmDelete(false)}>Cancel deletion</button>
        </div>}
      </div>}
    </>}
  </section>;
}
