import { test } from 'node:test';
import assert from 'node:assert/strict';
import { normalizeEstablishments, preserveWebsites } from './prepare-establishments.mjs';
import { matchWebsites } from './match-establishment-websites.mjs';

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

test('matches only credible names, locations and website URLs, and skips conflicts', () => {
  const feature = {
    type: 'Feature', geometry: { type: 'Point', coordinates: [-122.3, 47.6] },
    properties: { name: 'Portage Bay Cafe', address: '4130 Roosevelt Way NE', city: 'Seattle', zipcode: '98105' },
  };
  const directory = { type: 'FeatureCollection', features: [feature] };
  const candidate = {
    id: 'place-1', name: 'Portage Bay Cafe', address: '4130 Roosevelt Way NE', lat: 47.6001, lng: -122.3,
    websites: ['https://portagebaycafe.com/?tracking=yes'], sources: [{ property: '', license: 'CDLA-Permissive-2.0' }],
  };
  const result = matchWebsites(directory, [candidate]);
  assert.equal(result.matched, 1);
  assert.equal(result.data.features[0].properties.website, 'https://portagebaycafe.com/');
  assert.equal(result.data.features[0].properties.websiteSourceId, 'place-1');
  for (const change of [
    { name: 'Another Cafe' }, { lat: 48 }, { address: '4132 Roosevelt Way NE' },
    { address: '4130 Stone Way NE' }, { websites: ['javascript:alert(1)'] },
    { websites: ['https://portagebaycafe.facebook.com/'] }, { websites: ['https://evil.com/'] },
    { websites: ['https://portagebaycafe.usdirectory.com/'] },
    { websites: ['https://user:password@portagebaycafe.com/'] },
    { websites: ['https://portagebaycafe.internal/'] },
    { sources: [{ property: '', license: 'unknown' }] },
  ]) {
    assert.equal(matchWebsites(directory, [{ ...candidate, ...change }]).matched, 0);
  }
  assert.equal(matchWebsites(directory, [candidate, { ...candidate, id: 'other', websites: ['https://portagebay.com/'] }]).matched, 0);
  assert.equal(matchWebsites(directory, [{ ...candidate, lat: 47.6009 }]).matched, 1);
  assert.equal(matchWebsites(directory, [{ ...candidate, lat: 47.6009, address: '4130 Stone Way NE' }]).matched, 0);
  const withExisting = result.data;
  assert.equal(matchWebsites(withExisting, []).data.features[0].properties.website, 'https://portagebaycafe.com/');
});

test('keeps links when the same directory record is regenerated', () => {
  const raw = [{ name: 'Cafe', address: '1 Main', city: 'Seattle', zipcode: '98101', latitude: 47.6, longitude: -122.3 }];
  const { data } = normalizeEstablishments(raw);
  const previous = structuredClone(data);
  previous.features[0].properties.website = 'https://cafe.example/';
  previous.features[0].properties.websiteSourceId = 'place-1';
  assert.equal(preserveWebsites(data, previous), 1);
  assert.equal(data.features[0].properties.website, 'https://cafe.example/');
  const changed = normalizeEstablishments([{ ...raw[0], address: '2 Main' }]).data;
  assert.equal(preserveWebsites(changed, previous), 0);
});
