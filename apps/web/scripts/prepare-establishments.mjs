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

async function prepare() {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
  const input = resolve(root, 'data', 'king_county_food_establishments.json');
  const output = resolve(root, 'apps', 'web', 'public', 'king-county-establishments.json');
  const { data, skipped } = normalizeEstablishments(JSON.parse(await readFile(input, 'utf8')));
  await mkdir(dirname(output), { recursive: true });
  await writeFile(output, JSON.stringify(data));
  console.log(`Prepared ${data.features.length} establishment locations; skipped ${skipped} invalid records.`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  prepare().catch(error => { console.error('Unable to prepare establishment data:', error); process.exitCode = 1; });
}
