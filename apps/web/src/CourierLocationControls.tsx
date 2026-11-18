import { useEffect, useRef, useState } from 'react';
import { api, ApiError, errorMessage } from './api';
import type { Order, Session, Store } from './types';

type Props = {
  session: Session; order: Order; store?: Store; busy: boolean;
  onLocation: (order: Order, location: { lat: number; lng: number }) => void;
};

function currentPosition(): Promise<GeolocationPosition> {
  return new Promise((resolve, reject) => {
    navigator.geolocation.getCurrentPosition(resolve, error => {
      reject(new Error(error.code === 1 ? 'Location permission was denied.' : 'Your location could not be determined.'));
    }, { enableHighAccuracy: true, maximumAge: 0, timeout: 10000 });
  });
}

export function CourierLocationControls({ session, order, store, busy, onLocation }: Props) {
  const [mode, setMode] = useState<'loading' | 'demo' | 'live' | 'error'>('loading');
  const [sharing, setSharing] = useState(false);
  const [message, setMessage] = useState('');
  const active = useRef(false);
  const sharingId = useRef<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mounted = useRef(false);
  const generation = useRef(0);
  const base = `/api/orders/${encodeURIComponent(order.id)}`;

  async function endSession(id: string) {
    try {
      await api(`${base}/location-stop`, session, { method: 'POST', body: { sharingId: id } });
    } catch (error) {
      console.error('Could not end location sharing:', error);
      if (mounted.current) setMessage('Could not notify the service. Your last location will expire automatically.');
    }
  }

  function stop() {
    active.current = false;
    generation.current += 1;
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null;
    const id = sharingId.current;
    sharingId.current = null;
    if (mounted.current) setSharing(false);
    if (id) void endSession(id);
  }

  useEffect(() => {
    mounted.current = true;
    const controller = new AbortController();
    api<{ role: string; actorId: string }>('/api/me', session, { signal: controller.signal })
      .then(identity => {
        if (identity.role !== 'courier' || identity.actorId !== session.actorId) throw new Error('Courier identity does not match this session.');
        if (mounted.current) setMode('live');
      }).catch(error => {
        if (controller.signal.aborted) return;
        if (error instanceof ApiError && error.status === 404) setMode('demo');
        else { setMode('error'); setMessage(errorMessage(error)); }
      });
    const onVisibility = () => {
      if (document.visibilityState !== 'visible' && active.current) {
        stop();
        if (mounted.current) setMessage('Location sharing stopped because this page is no longer visible.');
      }
    };
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      mounted.current = false;
      controller.abort();
      document.removeEventListener('visibilitychange', onVisibility);
      stop();
    };
  }, [session.token, session.actorId, order.id]);

  function start() {
    if (active.current || busy) return;
    if (!window.isSecureContext || !navigator.geolocation) {
      setMessage('Live location requires a secure connection and a browser with location support.');
      return;
    }
    if (document.visibilityState !== 'visible') {
      setMessage('Keep this page visible while sharing your location.');
      return;
    }
    active.current = true;
    const run = ++generation.current;
    setSharing(true);
    setMessage('');
    async function update() {
      try {
        const position = await currentPosition();
        if (!active.current || generation.current !== run) return;
        if (!sharingId.current) {
          const started = await api<{ sharingId: string }>(`${base}/location-start`, session, { method: 'POST', body: {} });
          if (typeof started.sharingId !== 'string' || !started.sharingId) throw new Error('The service returned an invalid sharing session.');
          if (!active.current || generation.current !== run) { await endSession(started.sharingId); return; }
          sharingId.current = started.sharingId;
        }
        await api(`${base}/location`, session, {
          method: 'POST', body: { lat: position.coords.latitude, lng: position.coords.longitude, sharingId: sharingId.current },
        });
        if (active.current && generation.current === run) timer.current = setTimeout(() => void update(), 15000);
      } catch (error) {
        if (active.current && generation.current === run) {
          stop();
          if (mounted.current) setMessage(`Location sharing stopped: ${errorMessage(error)}`);
        }
      }
    }
    void update();
  }

  if (mode === 'loading') return null;
  if (mode === 'error') return <p className="notice error" role="alert">Location controls unavailable: {message}</p>;
  if (mode === 'demo') return <div className="simulation-tools"><span className="eyebrow">MANUAL LOCATION SIMULATION</span><div>
    <button className="secondary" disabled={busy} onClick={() => onLocation(order, { lat: store?.lat ?? 47.607, lng: store?.lng ?? -122.337 })}>At kitchen</button>
    <button className="secondary" disabled={busy} onClick={() => onLocation(order, { lat: 47.6072, lng: -122.3345 })}>Halfway</button>
    <button className="secondary" disabled={busy} onClick={() => onLocation(order, { lat: 47.6067, lng: -122.3325 })}>At demo destination</button>
  </div><small>Illustrative coordinates only, not the entered delivery address.</small></div>;

  return <div className="simulation-tools">
    <span className="eyebrow">LIVE LOCATION SHARING</span>
    <small>Share your location with this order's customer and merchant while this page is visible. Map providers receive the area being viewed. Sharing stops when you leave this page or the delivery ends.</small>
    <div>{sharing
      ? <button className="secondary" onClick={stop}>Stop sharing location</button>
      : <button className="secondary" disabled={busy} onClick={start}>Share live location</button>}</div>
    {message && <p className="notice error" role="alert">{message}</p>}
  </div>;
}
