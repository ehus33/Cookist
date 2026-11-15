import { useEffect, useState } from 'react';
import { api, errorMessage } from '../api.js';
import type { Meal } from '../types.js';

export function useStorefront() {
  const [meals, setMeals] = useState<Meal[] | null>(null);
  const [loadError, setLoadError] = useState('');
  const [busy, setBusy] = useState<number | null>(null);
  const checkout = new URLSearchParams(location.search).get('checkout');
  const [notice, setNotice] = useState(checkout === 'success'
    ? 'Checkout completed. Payment confirmation is processed separately. This was a test; no food will be provided.'
    : checkout === 'cancelled' ? 'Checkout cancelled. No test payment was made.' : '');
  const [noticeError, setNoticeError] = useState(false);

  useEffect(() => {
    let active = true;
    api<Meal[]>('/api/meals').then(data => { if (active) setMeals(data); })
      .catch(() => { if (active) setLoadError('Could not load the menu.'); });
    return () => { active = false; };
  }, []);

  async function checkoutMeal(meal: Meal) {
    setBusy(meal.id);
    try {
      const result = await api<{ url: string }>('/api/checkout', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mealId: meal.id, quantity: 1 })
      });
      window.location.assign(result.url);
    } catch (error) {
      setNotice(errorMessage(error));
      setNoticeError(true);
      setBusy(null);
    }
  }

  return { meals, loadError, busy, notice, noticeError, checkoutMeal };
}
