import { test } from 'node:test';
import assert from 'node:assert/strict';
import { normalizeEstablishments } from './prepare-establishments.mjs';

test('keeps only display fields and valid locations', () => {
  const { data, skipped } = normalizeEstablishments([
    { name: ' Cafe ', address: ' 1 Main ', city: 'Seattle', zipcode: '98101', latitude: 47.6, longitude: -122.3, phone: 'private' },
    { name: 'Bad coordinates', latitude: 91, longitude: -122 },
    { name: ' ', latitude: 47, longitude: -122 },
    { name: 'Missing', latitude: null, longitude: -122 },
  ]);
  assert.equal(skipped, 3);
  assert.deepEqual(data.features, [{
    type: 'Feature', geometry: { type: 'Point', coordinates: [-122.3, 47.6] },
    properties: { name: 'Cafe', address: '1 Main', city: 'Seattle', zipcode: '98101' },
  }]);
});

test('rejects invalid or unusable input instead of publishing an empty map', () => {
  assert.throws(() => normalizeEstablishments({}), /must be an array/);
  assert.throws(() => normalizeEstablishments([]), /no usable locations/);
});
