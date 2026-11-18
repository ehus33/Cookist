import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const excludedHosts = [
  'facebook.com', 'instagram.com', 'tiktok.com', 'x.com', 'twitter.com', 'yelp.com',
  'tripadvisor.com', 'google.com', 'googleusercontent.com', 'doordash.com', 'ubereats.com',
  'grubhub.com', 'postmates.com', 'ordertogo.com', 'table22.com', 'linktr.ee',
  'hub.biz', 'goodbistros.com', 'business.site', 'restaurantji.com', 'allmenus.com', 'menupix.com',
  'usdirectory.com', 'teriyakian.com',
];
const genericWords = new Set([
  'the', 'and', 'restaurant', 'restaurants', 'cafe', 'coffee', 'bar', 'grill', 'kitchen',
  'bakery', 'food', 'foods', 'shop', 'store', 'company', 'inc', 'llc', 'seattle',
]);

function normalize(value) {
  return value.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase()
    .replace(/&/g, ' and ').replace(/['’]/g, '').replace(/[^\p{L}\p{N}]+/gu, ' ').trim().replace(/\s+/g, ' ');
}

function houseNumber(address) {
  return (address ?? '').match(/^\s*(\d+[a-z]?)/i)?.[1]?.toLowerCase();
}

function sameStreet(first, second) {
  const common = new Set(['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw', 'street', 'st',
    'avenue', 'ave', 'way', 'wy', 'road', 'rd', 'boulevard', 'blvd', 'drive', 'dr',
    'court', 'ct', 'suite', 'ste', 'apt', 'unit', 'floor']);
  const tokens = address => normalize(address).split(' ').slice(1)
    .filter(token => token.length > 1 && !common.has(token) && !/^\d+$/.test(token));
  const other = new Set(tokens(second));
  return tokens(first).some(token => other.has(token));
}

function distanceMeters([lng, lat], candidate) {
  const radians = Math.PI / 180;
  const dy = (lat - candidate.lat) * radians;
  const dx = (lng - candidate.lng) * radians;
  const a = Math.sin(dy / 2) ** 2 + Math.cos(lat * radians) * Math.cos(candidate.lat * radians) * Math.sin(dx / 2) ** 2;
  return 12742000 * Math.asin(Math.min(1, Math.sqrt(a)));
}

function websiteFor(name, value) {
  if (typeof value !== 'string' || value.length > 2048 || !/^https?:\/\//i.test(value)
    || /[\u0000-\u0020\u007f]/.test(value)) return null;
  let url;
  try { url = new URL(value); } catch { return null; }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.port) return null;
  const host = url.hostname.toLowerCase();
  if (!/^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$/.test(host)
    || ['localhost', 'local', 'internal'].some(suffix => host.endsWith(`.${suffix}`))
    || excludedHosts.some(excluded => host === excluded || host.endsWith(`.${excluded}`))) return null;
  const distinctive = normalize(name).split(' ').filter(token => token.length >= 4 && !genericWords.has(token));
  const digits = normalize(name).split(' ').filter(token => /^\d{3,}$/.test(token));
  const hostText = host.replace(/[^a-z0-9]/g, '');
  if (![...distinctive, ...digits].some(token => hostText.includes(token))) return null;
  url.search = '';
  url.hash = '';
  return url.href;
}

export function matchWebsites(directory, candidates) {
  if (directory?.type !== 'FeatureCollection' || !Array.isArray(directory.features) || !Array.isArray(candidates)) {
    throw new Error('Expected a GeoJSON directory and an array of Overture places.');
  }
  const index = new Map();
  for (const candidate of candidates) {
    if (!candidate || typeof candidate.name !== 'string' || typeof candidate.id !== 'string'
      || !Number.isFinite(candidate.lat) || !Number.isFinite(candidate.lng)
      || !Array.isArray(candidate.websites) || !Array.isArray(candidate.sources)) continue;
    const source = candidate.sources.find(item => item?.property === '');
    if (!source || !['CDLA-Permissive-2.0', 'CC0-1.0'].includes(source.license)) continue;
    const key = normalize(candidate.name);
    if (!index.has(key)) index.set(key, []);
    index.get(key).push(candidate);
  }
  let matched = 0;
  const features = directory.features.map(feature => {
    if (feature?.type !== 'Feature' || feature.geometry?.type !== 'Point'
      || !Array.isArray(feature.geometry.coordinates) || feature.geometry.coordinates.length !== 2
      || !feature.geometry.coordinates.every(Number.isFinite)
      || typeof feature.properties?.name !== 'string' || typeof feature.properties?.address !== 'string') {
      throw new Error('Directory contains an invalid feature.');
    }
    const properties = { ...feature.properties };
    if (properties.website) return { ...feature, properties };
    const candidatesForName = index.get(normalize(properties.name)) ?? [];
    const matches = [];
    for (const candidate of candidatesForName) {
      const distance = distanceMeters(feature.geometry.coordinates, candidate);
      const house = houseNumber(properties.address);
      const candidateHouse = houseNumber(candidate.address);
      if (house && candidateHouse && house !== candidateHouse) continue;
      if (house && candidateHouse && !sameStreet(properties.address, candidate.address)) continue;
      if (distance > 50 && !(distance <= 150 && house && house === candidateHouse)) continue;
      for (const site of candidate.websites) {
        const website = websiteFor(properties.name, site);
        if (website) matches.push({ website, id: candidate.id });
      }
    }
    const uniqueUrls = new Set(matches.map(match => match.website.replace(/\/$/, '')));
    if (uniqueUrls.size === 1) {
      properties.website = matches[0].website;
      properties.websiteSourceId = matches[0].id;
      matched++;
    }
    return { ...feature, properties };
  });
  return { data: { ...directory, features }, matched };
}

async function main() {
  if (process.argv.length !== 3) throw new Error('Usage: node scripts/match-establishment-websites.mjs <overture-places.json>');
  const directoryPath = fileURLToPath(new URL('../public/king-county-establishments.json', import.meta.url));
  const [directory, candidates] = await Promise.all([
    readFile(directoryPath, 'utf8').then(JSON.parse),
    readFile(resolve(process.argv[2]), 'utf8').then(JSON.parse),
  ]);
  const { data, matched } = matchWebsites(directory, candidates);
  await writeFile(directoryPath, JSON.stringify(data));
  const total = data.features.filter(feature => feature.properties.website).length;
  console.log(`Added ${matched} candidate websites; ${total} of ${data.features.length} listings now have links.`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(error => { console.error('Unable to match establishment websites:', error); process.exitCode = 1; });
}
