import { useEffect, useState } from 'react';
import { api, errorMessage } from './api';
import type { Order, Session } from './types';

export function useOrders(session: Session | null, revision: number) {
  const [orders, setOrders] = useState<Order[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    setOrders([]);
    setError('');
    if (!session) return;
    const controller = new AbortController();
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      setLoading(true);
      try {
        const data = await api<Order[]>('/api/orders', session, { signal: controller.signal });
        if (active) { setOrders(data); setError(''); }
      } catch (error) {
        if (active) setError(errorMessage(error));
      } finally {
        if (active) {
          setLoading(false);
          timer = setTimeout(poll, 4000);
        }
      }
    }
    void poll();
    return () => { active = false; controller.abort(); clearTimeout(timer); };
  }, [session, revision]);
  return { orders, error, loading };
}
