import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export function normalizeEstablishments(records) {
  if (!Array.isArray(records)) throw new Error('Establishment data must be an array.');
  const features = [];
  let skipped = 0;
  for (const record of records) {
    if (!record || typeof record.name !== 'string' || !record.name.trim()
      || typeof record.latitude !== 'number' || !Number.isFinite(record.latitude)
      || Math.abs(record.latitude) > 85.051129
      || typeof record.longitude !== 'number' || !Number.isFinite(record.longitude)
      || Math.abs(record.longitude) > 180) {
      skipped++;
      continue;
    }
    const text = value => typeof value === 'string' ? value.trim() : '';
    features.push({
      type: 'Feature',
      geometry: { type: 'Point', coordinates: [record.longitude, record.latitude] },
      properties: { name: record.name.trim(), address: text(record.address), city: text(record.city), zipcode: text(record.zipcode) },
    });
  }
  if (!features.length) throw new Error('Establishment data contains no usable locations.');
  return { data: { type: 'FeatureCollection', features }, skipped };
}

export function preserveWebsites(data, previous) {
  if (previous?.type !== 'FeatureCollection' || !Array.isArray(previous.features)) {
    throw new Error('Existing establishment directory is invalid.');
  }
  const key = feature => JSON.stringify([feature.geometry.coordinates, feature.properties.name,
    feature.properties.address, feature.properties.city, feature.properties.zipcode]);
  const saved = new Map();
  for (const feature of previous.features) {
    if (!feature.properties?.website) continue;
    const identity = key(feature);
    const existing = saved.get(identity);
    if (existing && (existing.website !== feature.properties.website || existing.websiteSourceId !== feature.properties.websiteSourceId)) {
      saved.set(identity, { conflict: true });
    } else if (!existing) {
      saved.set(identity, { website: feature.properties.website, websiteSourceId: feature.properties.websiteSourceId });
    }
  }
  let retained = 0;
  for (const feature of data.features) {
    const match = saved.get(key(feature));
    if (match && !match.conflict) {
      feature.properties.website = match.website;
      if (match.websiteSourceId) feature.properties.websiteSourceId = match.websiteSourceId;
      retained++;
    }
  }
  return retained;
}

async function prepare() {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
  const input = resolve(root, 'data', 'king_county_food_establishments.json');
  const output = resolve(root, 'apps', 'web', 'public', 'king-county-establishments.json');
  const { data, skipped } = normalizeEstablishments(JSON.parse(await readFile(input, 'utf8')));
  let previous;
  try { previous = JSON.parse(await readFile(output, 'utf8')); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  const retained = previous ? preserveWebsites(data, previous) : 0;
  const previousCount = previous?.features?.filter(feature => feature.properties?.website).length ?? 0;
  await mkdir(dirname(output), { recursive: true });
  await writeFile(output, JSON.stringify(data));
  console.log(`Prepared ${data.features.length} establishment locations; skipped ${skipped} invalid records; retained ${retained} of ${previousCount} website links.`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  prepare().catch(error => { console.error('Unable to prepare establishment data:', error); process.exitCode = 1; });
}
