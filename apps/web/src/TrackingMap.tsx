import { useEffect, useRef, useState } from 'react';
import { AttributionControl, LngLatBounds, Map, Marker, NavigationControl, setWorkerUrl } from 'maplibre-gl';
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';
import 'maplibre-gl/dist/maplibre-gl.css';
import type { Order, Store } from './types';

setWorkerUrl(workerUrl);

type Position = { lat: number; lng: number };
function isMappable(position: Position | null | undefined): position is Position {
  return !!position && Number.isFinite(position.lat) && Number.isFinite(position.lng)
    && Math.abs(position.lat) <= 85.051129 && Math.abs(position.lng) <= 180;
}

function marker(label: string, className: string, offset: [number, number] = [0, 0]) {
  const element = document.createElement('span');
  element.className = `tracking-marker ${className}`;
  element.textContent = label === 'Demo kitchen' ? 'K' : 'C';
  element.setAttribute('role', 'img');
  element.setAttribute('aria-label', label);
  element.title = label;
  return new Marker({ element, offset });
}

export function TrackingMap({ store, location }: { store?: Store; location: Order['location'] }) {
  const container = useRef<HTMLDivElement>(null);
  const mapRef = useRef<Map | null>(null);
  const initialPosition = useRef<Position>(isMappable(store) ? store : isMappable(location) ? location : { lat: 47.607, lng: -122.337 });
  const [mapError, setMapError] = useState('');
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!container.current) return;
    let map: Map | undefined;
    try {
      map = new Map({
        container: container.current,
        style: 'https://tiles.openfreemap.org/styles/liberty',
        center: [initialPosition.current.lng, initialPosition.current.lat],
        zoom: 13,
        attributionControl: false,
      });
      map.addControl(new NavigationControl({ showCompass: false }), 'top-right');
      map.addControl(new AttributionControl({ compact: false }), 'bottom-right');
      map.on('error', event => {
        console.error('Map failed to load:', event.error);
        setMapError('Some map data could not be loaded. Check your connection and try again.');
      });
      mapRef.current = map;
    } catch (error) {
      map?.remove();
      console.error('Map could not be initialized:', error);
      setMapError('The map could not be displayed. Check your browser and try again.');
      return;
    }
    return () => { mapRef.current = null; map.remove(); };
  }, [attempt]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    const positions: (Position | null)[] = [isMappable(store) ? store : null, isMappable(location) ? location : null];
    const overlap = positions[0] && positions[1] && positions[0].lat === positions[1].lat && positions[0].lng === positions[1].lng;
    const markers = positions.flatMap((position, index) => position
      ? [marker(index === 0 ? 'Demo kitchen' : location?.source === 'live' ? 'Live courier' : 'Simulated courier', index === 0 ? 'kitchen-marker' : 'courier-marker',
        index === 1 && overlap ? [15, -15] : [0, 0])
        .setLngLat([position.lng, position.lat]).addTo(map)] : []);
    const available = positions.filter((position): position is Position => position !== null);
    if (available.length === 1) {
      map.jumpTo({ center: [available[0].lng, available[0].lat], zoom: 13 });
    } else if (available.length === 2) {
      const bounds = new LngLatBounds()
        .extend([available[0].lng, available[0].lat])
        .extend([available[1].lng, available[1].lat]);
      map.fitBounds(bounds, { padding: 45, maxZoom: 14, duration: 0 });
    }
    return () => { markers.forEach(item => item.remove()); };
  }, [store?.lat, store?.lng, location?.lat, location?.lng, location?.source, attempt]);

  return <>
    <div className="tracking-map" ref={container} aria-label="Interactive map of demo locations" />
    {mapError && <div className="map-error" role="alert">{mapError} <button type="button" className="text-button" onClick={() => { setMapError(''); setAttempt(value => value + 1); }}>Retry map</button></div>}
    {!isMappable(store) && <p className="map-warning">Kitchen coordinates cannot be shown on the map.</p>}
    {location && !isMappable(location) && <p className="map-warning">The courier coordinates cannot be shown on the map.</p>}
  </>;
}
