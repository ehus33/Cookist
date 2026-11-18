import { useEffect, useMemo, useRef, useState } from 'react';
import { AttributionControl, Map, Marker, NavigationControl, setWorkerUrl } from 'maplibre-gl';
import type { GeoJSONSource } from 'maplibre-gl';
import type { Feature, FeatureCollection, Point } from 'geojson';
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';
import 'maplibre-gl/dist/maplibre-gl.css';

setWorkerUrl(workerUrl);

type Details = { name: string; address: string; city: string; zipcode: string; website?: unknown };
type Listing = Feature<Point, Details>;
type Listings = FeatureCollection<Point, Details>;

function safeWebsite(value: unknown): string | null {
  if (typeof value !== 'string' || value.length > 2048 || !/^https?:\/\//i.test(value)
    || /[\u0000-\u0020\u007f]/.test(value)) return null;
  try {
    const url = new URL(value);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.port
      || !/^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$/i.test(url.hostname)
      || ['localhost', 'local', 'internal'].some(suffix => url.hostname.endsWith(`.${suffix}`))) return null;
    return url.href;
  } catch {
    return null;
  }
}

function isListings(value: unknown): value is Listings {
  if (!value || typeof value !== 'object' || !('type' in value) || value.type !== 'FeatureCollection'
    || !('features' in value) || !Array.isArray(value.features)) return false;
  return value.features.every((feature: unknown) => {
    if (!feature || typeof feature !== 'object' || !('type' in feature) || feature.type !== 'Feature'
      || !('geometry' in feature) || !feature.geometry || typeof feature.geometry !== 'object'
      || !('type' in feature.geometry) || feature.geometry.type !== 'Point'
      || !('coordinates' in feature.geometry) || !Array.isArray(feature.geometry.coordinates)
      || feature.geometry.coordinates.length !== 2) return false;
    const [lng, lat] = feature.geometry.coordinates;
    if (typeof lng !== 'number' || !Number.isFinite(lng) || Math.abs(lng) > 180
      || typeof lat !== 'number' || !Number.isFinite(lat) || Math.abs(lat) > 85.051129) return false;
    if (!('properties' in feature) || !feature.properties || typeof feature.properties !== 'object') return false;
    const properties = feature.properties;
    return 'name' in properties && typeof properties.name === 'string' && !!properties.name.trim()
      && 'address' in properties && typeof properties.address === 'string'
      && 'city' in properties && typeof properties.city === 'string'
      && 'zipcode' in properties && typeof properties.zipcode === 'string';
  });
}

function DirectoryMap({ data, onSelect, mapRef }: {
  data: Listings;
  onSelect: (listing: Listing) => void;
  mapRef: React.RefObject<Map | null>;
}) {
  const container = useRef<HTMLDivElement>(null);
  const currentData = useRef(data);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [tracking, setTracking] = useState(false);
  const [position, setPosition] = useState<{ lat: number; lng: number } | null>(null);
  const [locationMessage, setLocationMessage] = useState('');
  const watchId = useRef<number | null>(null);
  const locationActive = useRef(false);
  const locationGeneration = useRef(0);
  currentData.current = data;

  function cancelWatch() {
    locationActive.current = false;
    locationGeneration.current++;
    if (watchId.current !== null) navigator.geolocation.clearWatch(watchId.current);
    watchId.current = null;
  }

  function stopLocation() {
    cancelWatch();
    setTracking(false);
    setPosition(null);
  }

  useEffect(() => {
    const onVisibilityChange = () => {
      if (document.visibilityState !== 'visible' && locationActive.current) {
        stopLocation();
        setLocationMessage('Location stopped because this page is no longer visible.');
      }
    };
    document.addEventListener('visibilitychange', onVisibilityChange);
    return () => {
      document.removeEventListener('visibilitychange', onVisibilityChange);
      cancelWatch();
    };
  }, []);

  useEffect(() => {
    if (!container.current) return;
    let map: Map | undefined;
    try {
      map = new Map({
        container: container.current,
        style: 'https://tiles.openfreemap.org/styles/liberty',
        center: [-122.2, 47.55],
        zoom: 8.5,
        attributionControl: false,
      });
      const activeMap = map;
      activeMap.addControl(new NavigationControl({ showCompass: false }), 'top-right');
      activeMap.addControl(new AttributionControl({ compact: false }), 'bottom-right');
      activeMap.on('error', event => {
        console.error('Directory map failed to load:', event.error);
        setError('Some map data could not be loaded. Check your connection and try again.');
      });
      activeMap.on('load', () => {
        activeMap.addSource('establishments', { type: 'geojson', data: currentData.current, cluster: true, clusterRadius: 45, clusterMaxZoom: 15 });
        activeMap.addLayer({
          id: 'establishment-clusters', type: 'circle', source: 'establishments',
          filter: ['has', 'point_count'],
          paint: { 'circle-color': '#526444', 'circle-radius': ['step', ['get', 'point_count'], 18, 100, 23, 1000, 29], 'circle-stroke-width': 2, 'circle-stroke-color': '#fffdf7' },
        });
        activeMap.addLayer({
          id: 'establishment-cluster-count', type: 'symbol', source: 'establishments',
          filter: ['has', 'point_count'],
          layout: { 'text-field': ['get', 'point_count_abbreviated'], 'text-font': ['Noto Sans Regular'], 'text-size': 12 },
          paint: { 'text-color': '#ffffff' },
        });
        activeMap.addLayer({
          id: 'establishment-points', type: 'circle', source: 'establishments',
          filter: ['!', ['has', 'point_count']],
          paint: { 'circle-color': '#d94f2b', 'circle-radius': 6, 'circle-stroke-width': 2, 'circle-stroke-color': '#fffdf7' },
        });
        activeMap.on('click', 'establishment-clusters', event => {
          const feature = event.features?.[0];
          if (!feature || feature.geometry.type !== 'Point' || typeof feature.properties?.cluster_id !== 'number') return;
          const coordinates = feature.geometry.coordinates;
          const source = activeMap.getSource('establishments') as GeoJSONSource;
          void source.getClusterExpansionZoom(feature.properties.cluster_id).then(zoom => {
            activeMap.easeTo({ center: [coordinates[0], coordinates[1]], zoom });
          }).catch(cause => {
            console.error('Could not expand establishment cluster:', cause);
            setError('Could not expand this cluster. Try again.');
          });
        });
        activeMap.on('click', 'establishment-points', event => {
          const feature = event.features?.[0];
          if (!feature || feature.geometry.type !== 'Point' || !feature.properties) return;
          const properties = feature.properties;
          if (typeof properties.name !== 'string' || typeof properties.address !== 'string'
            || typeof properties.city !== 'string' || typeof properties.zipcode !== 'string') return;
          onSelect({ type: 'Feature', geometry: feature.geometry, properties: {
            name: properties.name, address: properties.address, city: properties.city, zipcode: properties.zipcode,
            website: properties.website,
          } });
        });
      });
      mapRef.current = activeMap;
    } catch (cause) {
      map?.remove();
      console.error('Directory map could not be initialized:', cause);
      setError('The map could not be displayed. Check your browser and try again.');
      return;
    }
    return () => { mapRef.current = null; map?.remove(); };
  }, [attempt, mapRef, onSelect]);

  useEffect(() => {
    const source = mapRef.current?.getSource('establishments') as GeoJSONSource | undefined;
    source?.setData(data);
  }, [data, mapRef]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || !position) return;
    const element = document.createElement('span');
    element.className = 'directory-location-marker';
    element.setAttribute('role', 'img');
    element.setAttribute('aria-label', 'Your current location');
    const marker = new Marker({ element }).setLngLat([position.lng, position.lat]).addTo(map);
    return () => { marker.remove(); };
  }, [position, attempt, mapRef]);

  function centerLocation(location: { lat: number; lng: number }) {
    mapRef.current?.easeTo({ center: [location.lng, location.lat], zoom: 14 });
  }

  function startLocation() {
    if (locationActive.current) return;
    if (!window.isSecureContext || !navigator.geolocation) {
      setLocationMessage('Location requires a secure connection and a browser with location support.');
      return;
    }
    if (!mapRef.current) {
      setLocationMessage('The map is unavailable. Retry the map before using your location.');
      return;
    }
    if (document.visibilityState !== 'visible') {
      setLocationMessage('Keep this page visible while using your location.');
      return;
    }
    locationActive.current = true;
    const generation = ++locationGeneration.current;
    let firstFix = true;
    setTracking(true);
    setLocationMessage('');
    try {
      const id = navigator.geolocation.watchPosition(result => {
        if (!locationActive.current || generation !== locationGeneration.current) return;
        const { latitude: lat, longitude: lng } = result.coords;
        if (!Number.isFinite(lat) || Math.abs(lat) > 85.051129 || !Number.isFinite(lng) || Math.abs(lng) > 180) {
          stopLocation();
          setLocationMessage('Your location could not be shown on this map. Try again.');
          return;
        }
        const nextPosition = { lat, lng };
        setPosition(nextPosition);
        if (firstFix) { firstFix = false; centerLocation(nextPosition); }
      }, cause => {
        if (!locationActive.current || generation !== locationGeneration.current) return;
        stopLocation();
        setLocationMessage(cause.code === 1
          ? 'Location permission was denied. Allow location in your browser site settings, then try again.'
          : 'Your location could not be determined. Check your device settings and try again.');
      }, { enableHighAccuracy: true, maximumAge: 0, timeout: 15000 });
      if (locationActive.current && generation === locationGeneration.current) watchId.current = id;
      else navigator.geolocation.clearWatch(id);
    } catch (cause) {
      console.error('Could not start location:', cause);
      stopLocation();
      setLocationMessage('Could not start location. Check your browser settings and try again.');
    }
  }

  return <>
    <div className="directory-location">
      <p>Your location stays in this browser; OpenFreeMap can see the approximate map area. Location stops when you hide this map or leave the page.</p>
      <div>
        {tracking
          ? <button className="secondary" type="button" onClick={() => { stopLocation(); setLocationMessage(''); }}>Stop using location</button>
          : <button className="secondary" type="button" onClick={startLocation}>Use my location</button>}
        {position && <button className="secondary" type="button" onClick={() => centerLocation(position)}>Center on my location</button>}
      </div>
      {tracking && <p role="status">{position ? 'Your location is updating while this page is visible.' : 'Waiting for location permission or a location fix...'}</p>}
      {locationMessage && <p className="map-warning" role="alert">{locationMessage}</p>}
    </div>
    <div className="directory-map" ref={container} aria-label="Interactive King County establishment map" />
    {error && <div className="map-error" role="alert">{error} <button type="button" className="text-button" onClick={() => { setError(''); setAttempt(value => value + 1); }}>Retry map</button></div>}
  </>;
}

export function EstablishmentDirectory() {
  const [expanded, setExpanded] = useState(false);
  const [data, setData] = useState<Listings | null>(null);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<Listing | null>(null);
  const mapRef = useRef<Map | null>(null);

  useEffect(() => {
    if (!expanded || data) return;
    const controller = new AbortController();
    fetch(`${import.meta.env.BASE_URL}king-county-establishments.json`, { signal: controller.signal })
      .then(response => {
        if (!response.ok) throw new Error(`Directory request failed (${response.status}).`);
        return response.json() as Promise<unknown>;
      })
      .then(value => {
        if (!isListings(value)) throw new Error('Directory data is invalid.');
        if (!controller.signal.aborted) { setData(value); setError(''); }
      })
      .catch(cause => {
        if (!controller.signal.aborted) {
          console.error('Could not load establishment directory:', cause);
          setError('The establishment directory could not be loaded. Please try again.');
        }
      });
    return () => controller.abort();
  }, [expanded, data, attempt]);

  const filtered = useMemo(() => {
    if (!data) return null;
    const term = query.trim().toLocaleLowerCase();
    return { type: 'FeatureCollection' as const, features: term
      ? data.features.filter(({ properties }) => [properties.name, properties.address, properties.city, properties.zipcode]
        .some(value => value.toLocaleLowerCase().includes(term)))
      : data.features };
  }, [data, query]);

  function choose(listing: Listing) {
    setSelected(listing);
    const [lng, lat] = listing.geometry.coordinates;
    mapRef.current?.easeTo({ center: [lng, lat], zoom: 16 });
  }

  const selectedWebsite = safeWebsite(selected?.properties.website);
  return <section className="establishment-directory" aria-labelledby="directory-heading">
    <div className="directory-heading">
      <div><span className="eyebrow">EXPLORE KING COUNTY</span><h2 id="directory-heading">Food establishment directory</h2>
        <p>These scraped listings may be outdated or inaccurate. They are not verified Cookist partners and cannot be ordered from here.</p>
        <p>Candidate websites include data from <a href="https://overturemaps.org/" target="_blank" rel="noopener noreferrer">Overture Maps Foundation</a> and are not verified.</p></div>
      <button className="secondary" type="button" aria-expanded={expanded} aria-controls="directory-content" onClick={() => setExpanded(value => !value)}>{expanded ? 'Hide directory map' : 'Explore directory map'}</button>
    </div>
    {expanded && <div id="directory-content">
      {!data && !error && <p className="directory-status" role="status">Loading establishment directory...</p>}
      {error && <div className="notice error" role="alert">{error}<button className="secondary" type="button" onClick={() => { setError(''); setAttempt(value => value + 1); }}>Retry directory</button></div>}
      {data && filtered && <>
        <label className="directory-search">Search directory by name, address, city, or ZIP
          <input type="search" value={query} onChange={event => { setQuery(event.target.value); setSelected(null); }} placeholder="Try a name or city" />
        </label>
        <p className="directory-status" role="status">{filtered.features.length.toLocaleString()} listings match. Select a point or search for a listing.</p>
        <DirectoryMap data={filtered} onSelect={setSelected} mapRef={mapRef} />
        {selected && <div className="directory-detail" aria-live="polite">
          <strong>{selected.properties.name}</strong>
          <p>{[selected.properties.address, [selected.properties.city, selected.properties.zipcode].filter(Boolean).join(' ')].filter(Boolean).join(', ') || 'Address unavailable'}</p>
          {selectedWebsite && <p><a href={selectedWebsite} target="_blank" rel="noopener noreferrer">Visit unverified website</a></p>}
          <p>Directory listing only; not available to order.</p>
        </div>}
        {query.trim() && <div className="directory-results" aria-label="Directory search results">
          {filtered.features.slice(0, 25).map((listing, index) => <button type="button" key={index} onClick={() => choose(listing)}>
            <strong>{listing.properties.name}</strong><span>{[listing.properties.address, listing.properties.city].filter(Boolean).join(', ')}</span>
          </button>)}
          {filtered.features.length > 25 && <p>Showing the first 25 matches. Refine your search to see others.</p>}
        </div>}
      </>}
    </div>}
  </section>;
}
