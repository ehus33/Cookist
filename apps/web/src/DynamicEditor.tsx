import { useRef } from 'react';
import { defaultDynamicCollection, dynamicError, type DynamicCollection, type DynamicConfig, type DynamicField } from './dynamicApps';

export interface DynamicEditorProps {
  value: DynamicConfig | undefined;
  onChange: (value: DynamicConfig | undefined) => void;
  disabled: boolean;
  editing?: boolean;
}

export function DynamicEditor({ value, onChange, disabled, editing = false }: DynamicEditorProps) {
  const original = useRef({ editing, ids: new Set(editing ? value?.collections.map(collection => collection.id) : []) });
  if (original.current.editing !== editing) {
    original.current = { editing, ids: new Set(editing ? value?.collections.map(collection => collection.id) : []) };
  }
  const collections = value?.collections ?? [];
  const enabled = collections.length > 0;
  const locked = editing && original.current.ids.size > 0;
  function update(index: number, patch: Partial<DynamicCollection>) {
    onChange({ collections: collections.map((collection, i) => i === index ? { ...collection, ...patch } : collection) });
  }
  function updateField(index: number, fieldIndex: number, patch: Partial<DynamicField>) {
    update(index, { fields: collections[index].fields.map((field, i) => i === fieldIndex ? { ...field, ...patch } : field) });
  }
  const error = dynamicError(value);
  return <section className="dynamic-data-panel" aria-label="Data configuration">
    <h3>Data and member accounts</h3>
    <p>Visitor accounts belong to this site and are separate from Cookist owner accounts. Members sign in to use the collections you allow; Cookist owners manage records here.</p>
    <label><input type="checkbox" checked={enabled} disabled={disabled || locked}
      onChange={event => onChange(event.target.checked ? { collections: [defaultDynamicCollection()] } : undefined)} />Enable data and member accounts</label>
    {editing && <p>Existing collection schemas and permissions cannot be changed or removed. You can add new collections.</p>}
    {enabled && <>
      <h4>Data</h4>
      {collections.map((collection, index) => <fieldset key={index} disabled={disabled || (editing && original.current.ids.has(collection.id))}>
        <legend>Collection {index + 1}</legend>
        <label>Collection {index + 1} ID<input value={collection.id} maxLength={32} onChange={event => update(index, { id: event.target.value })} /></label>
        <label>Collection {index + 1} name<input value={collection.name} maxLength={80} onChange={event => update(index, { name: event.target.value })} /></label>
        <label>Collection {index + 1} read access<select value={collection.read} onChange={event => update(index, { read: event.target.value as DynamicCollection['read'] })}>
          <option value="public">Everyone</option><option value="members">Signed-in members</option><option value="own">Only the record's member</option>
        </select></label>
        <label>Collection {index + 1} create access<select value={collection.create} onChange={event => update(index, { create: event.target.value as DynamicCollection['create'] })}>
          <option value="members">Signed-in members</option><option value="none">Disabled</option>
        </select></label>
        {(['update', 'delete'] as const).map(permission => <label key={permission}>Collection {index + 1} {permission} access<select value={collection[permission]} onChange={event => update(index, { [permission]: event.target.value })}>
          <option value="own">Only the record's member</option><option value="none">Disabled</option>
        </select></label>)}
        {collection.fields.map((field, fieldIndex) => <div key={fieldIndex}>
          <label>Collection {index + 1} field {fieldIndex + 1} ID<input value={field.id} maxLength={32} onChange={event => updateField(index, fieldIndex, { id: event.target.value })} /></label>
          <label>Collection {index + 1} field {fieldIndex + 1} label<input value={field.label} maxLength={80} onChange={event => updateField(index, fieldIndex, { label: event.target.value })} /></label>
          <label>Collection {index + 1} field {fieldIndex + 1} type<select value={field.type} onChange={event => updateField(index, fieldIndex, { type: event.target.value as DynamicField['type'] })}>
            {(['text', 'email', 'number', 'boolean'] as const).map(type => <option key={type} value={type}>{type}</option>)}
          </select></label>
          <label><input type="checkbox" checked={field.required} onChange={event => updateField(index, fieldIndex, { required: event.target.checked })} />Collection {index + 1} field {fieldIndex + 1} required</label>
          <button type="button" onClick={() => update(index, { fields: collection.fields.filter((_, i) => i !== fieldIndex) })}>Remove field {fieldIndex + 1} from collection {index + 1}</button>
        </div>)}
        <button type="button" disabled={collection.fields.length >= 12} onClick={() => update(index, { fields: [...collection.fields, { id: '', label: '', type: 'text', required: false }] })}>Add field to collection {index + 1}</button>
        <button type="button" onClick={() => {
          const next = collections.filter((_, i) => i !== index);
          onChange(next.length ? { collections: next } : undefined);
        }}>Remove collection {index + 1}</button>
      </fieldset>)}
      <button type="button" disabled={disabled || collections.length >= 8} onClick={() => onChange({ collections: [...collections, { ...defaultDynamicCollection(''), name: '' }] })}>Add collection</button>
    </>}
    {error && <p role="alert">{error}</p>}
  </section>;
}
