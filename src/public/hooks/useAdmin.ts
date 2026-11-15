import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { api, errorMessage } from '../api.js';
import type { Order, Seller } from '../types.js';

function adminApi<T>(token: string, url: string, options: RequestInit = {}) {
  const headers = new Headers(options.headers);
  headers.set('Content-Type', 'application/json');
  headers.set('X-Admin-Token', token);
  return api<T>(url, { ...options, headers });
}

export function useAdmin() {
  const [token, setToken] = useState('');
  const [sellers, setSellers] = useState<Seller[]>([]);
  const [orders, setOrders] = useState<Order[]>([]);
  const [message, setMessage] = useState('');
  const [isError, setIsError] = useState(false);
  const [onboarding, setOnboarding] = useState<Record<number, string>>({});

  function notify(text: string, error = false) {
    setMessage(text);
    setIsError(error);
  }

  async function loadDashboard(value: string) {
    const [sellerData, orderData] = await Promise.all([
      adminApi<Seller[]>(value, '/api/admin/sellers'),
      adminApi<Order[]>(value, '/api/admin/orders')
    ]);
    setSellers(sellerData);
    setOrders(orderData);
    setOnboarding({});
  }

  useEffect(() => {
    const saved = sessionStorage.getItem('cookist-admin-token');
    if (!saved) return;
    let active = true;
    Promise.all([adminApi<Seller[]>(saved, '/api/admin/sellers'), adminApi<Order[]>(saved, '/api/admin/orders')])
      .then(([sellerData, orderData]) => {
        if (active) { setSellers(sellerData); setOrders(orderData); setToken(saved); }
      })
      .catch(error => {
        if (active) { sessionStorage.removeItem('cookist-admin-token'); notify(errorMessage(error), true); }
      });
    return () => { active = false; };
  }, []);

  async function unlock(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const value = new FormData(form).get('token');
    if (typeof value !== 'string') return notify('Enter an administrator token.', true);
    try {
      await loadDashboard(value);
      sessionStorage.setItem('cookist-admin-token', value);
      setToken(value);
      form.reset();
      notify('Admin unlocked.');
    } catch (error) { notify(errorMessage(error), true); }
  }

  async function addSeller(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    try {
      await adminApi(token, '/api/admin/sellers', { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(form))) });
      form.reset();
      await loadDashboard(token);
      notify('Seller created. Complete Stripe onboarding and independently verify the seller before approval.');
    } catch (error) { notify(errorMessage(error), true); }
  }

  async function addMeal(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const values = Object.fromEntries(new FormData(form));
    const meal = { sellerId: Number(values.sellerId), priceCents: Math.round(Number(values.price) * 100),
      name: values.name, description: values.description, allergens: values.allergens, pickup: values.pickup };
    try {
      await adminApi(token, '/api/admin/meals', { method: 'POST', body: JSON.stringify(meal) });
      form.reset();
      notify('Meal added.');
    } catch (error) { notify(errorMessage(error), true); }
  }

  async function createOnboarding(sellerId: number) {
    try {
      const result = await adminApi<{ url: string }>(token, `/api/admin/sellers/${sellerId}/onboarding`, { method: 'POST' });
      setOnboarding(previous => ({ ...previous, [sellerId]: result.url }));
      notify('Send the onboarding link securely to the seller.');
    } catch (error) { notify(errorMessage(error), true); }
  }

  async function toggleApproval(seller: Seller) {
    try {
      await adminApi(token, `/api/admin/sellers/${seller.id}`, {
        method: 'PATCH', body: JSON.stringify({ approved: !seller.approved })
      });
      await loadDashboard(token);
    } catch (error) { notify(errorMessage(error), true); }
  }

  function lock() {
    sessionStorage.removeItem('cookist-admin-token');
    setToken('');
    setSellers([]);
    setOrders([]);
    setOnboarding({});
    notify('Admin locked.');
  }

  return { token, sellers, orders, message, isError, onboarding, unlock, addSeller, addMeal, createOnboarding, toggleApproval, lock };
}
