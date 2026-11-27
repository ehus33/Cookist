import { expect, it } from 'vitest';
import { defaultDynamicCollection, dynamicError, isDynamicConfig } from './dynamicApps';

it('accepts disabled and valid configs, rejecting invalid IDs, duplicates and access choices', () => {
  expect(dynamicError(undefined)).toBeNull();
  expect(isDynamicConfig({ collections: [] })).toBe(true);
  const collection = defaultDynamicCollection();
  expect(isDynamicConfig({ collections: [collection] })).toBe(true);
  expect(isDynamicConfig({ collections: [{ ...collection, id: 'Bad' }] })).toBe(false);
  expect(dynamicError({ collections: [collection, collection] })).toMatch(/unique/);
  expect(isDynamicConfig({ collections: [{ ...collection, read: ['own'] }] })).toBe(false);
  expect(isDynamicConfig({ collections: [{ ...collection, fields: [{ ...collection.fields[0], required: 'yes' }] }] })).toBe(false);
  expect(dynamicError({ collections: [{ ...collection, fields: [collection.fields[0], collection.fields[0]] }] })).toMatch(/unique/);
});

it('enforces collection/field caps and nonblank names and labels', () => {
  const collection = defaultDynamicCollection();
  expect(isDynamicConfig({ collections: Array.from({ length: 9 }, (_, i) => ({ ...collection, id: `c${i}` })) })).toBe(false);
  expect(isDynamicConfig({ collections: [{ ...collection, fields: Array.from({ length: 13 }, (_, i) => ({ ...collection.fields[0], id: `f${i}` })) }] })).toBe(false);
  for (const name of ['', ' ', 'a'.repeat(81)]) expect(isDynamicConfig({ collections: [{ ...collection, name }] })).toBe(false);
  expect(isDynamicConfig({ collections: [{ ...collection, fields: [{ ...collection.fields[0], label: '' }] }] })).toBe(false);
  expect(isDynamicConfig(null)).toBe(false);
});
