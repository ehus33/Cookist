export interface DynamicField {
  id: string;
  label: string;
  type: 'text' | 'email' | 'number' | 'boolean';
  required: boolean;
}

export interface DynamicCollection {
  id: string;
  name: string;
  fields: DynamicField[];
  read: 'public' | 'members' | 'own';
  create: 'members' | 'none';
  update: 'own' | 'none';
  delete: 'own' | 'none';
}

export interface DynamicConfig { collections: DynamicCollection[] }

const identifier = /^[a-z][a-z0-9_-]{0,31}$/;
export function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function configError(value: unknown): string | null {
  if (!isObject(value) || !Array.isArray(value.collections)) return 'Data configuration must contain collections.';
  if (value.collections.length > 8) return 'Add no more than 8 collections.';
  const ids = new Set<string>();
  for (const [index, collection] of value.collections.entries()) {
    const prefix = `Collection ${index + 1}`;
    if (!isObject(collection)) return `${prefix} is invalid.`;
    if (typeof collection.id !== 'string' || !identifier.test(collection.id)) return `${prefix} ID must start with a lowercase letter and contain only lowercase letters, numbers, underscores or hyphens (maximum 32 characters).`;
    if (ids.has(collection.id)) return 'Collection IDs must be unique.';
    ids.add(collection.id);
    if (typeof collection.name !== 'string' || !collection.name.trim() || collection.name.length > 80) return `${prefix} needs a name between 1 and 80 characters.`;
    if (typeof collection.read !== 'string' || !['public', 'members', 'own'].includes(collection.read)
      || typeof collection.create !== 'string' || !['members', 'none'].includes(collection.create)
      || typeof collection.update !== 'string' || !['own', 'none'].includes(collection.update)
      || typeof collection.delete !== 'string' || !['own', 'none'].includes(collection.delete)) return `${prefix} has invalid access permissions.`;
    if (!Array.isArray(collection.fields) || collection.fields.length < 1 || collection.fields.length > 12) return `${prefix} needs between 1 and 12 fields.`;
    const fields = new Set<string>();
    for (const [fieldIndex, field] of collection.fields.entries()) {
      const fieldPrefix = `${prefix}, field ${fieldIndex + 1}`;
      if (!isObject(field)) return `${fieldPrefix} is invalid.`;
      if (typeof field.id !== 'string' || !identifier.test(field.id)) return `${fieldPrefix} ID must start with a lowercase letter and contain only lowercase letters, numbers, underscores or hyphens (maximum 32 characters).`;
      if (fields.has(field.id)) return `${prefix} field IDs must be unique.`;
      fields.add(field.id);
      if (typeof field.label !== 'string' || !field.label.trim() || field.label.length > 80) return `${fieldPrefix} needs a label between 1 and 80 characters.`;
      if (typeof field.type !== 'string' || !['text', 'email', 'number', 'boolean'].includes(field.type) || typeof field.required !== 'boolean') return `${fieldPrefix} has an invalid type or required setting.`;
    }
  }
  return null;
}

export function dynamicError(value: DynamicConfig | undefined): string | null {
  return value === undefined ? null : configError(value);
}

export function isDynamicConfig(value: unknown): value is DynamicConfig {
  return configError(value) === null;
}

export function defaultDynamicCollection(id = 'requests'): DynamicCollection {
  return {
    id, name: 'Requests', read: 'own', create: 'members', update: 'own', delete: 'own',
    fields: [
      { id: 'title', label: 'Title', type: 'text', required: true },
      { id: 'details', label: 'Details', type: 'text', required: false },
    ],
  };
}
