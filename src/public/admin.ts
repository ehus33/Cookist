export {};
const message = document.querySelector<HTMLElement>('#message')!;
const login = document.querySelector<HTMLFormElement>('#login')!;
const dashboard = document.querySelector<HTMLElement>('#dashboard')!;
const sellerForm = document.querySelector<HTMLFormElement>('#seller-form')!;
const mealForm = document.querySelector<HTMLFormElement>('#meal-form')!;
let token = sessionStorage.getItem('cookist-admin-token') || '';

interface Seller {
  id: number;
  name: string;
  approved: number;
  kitchen: string;
  permit_reference: string;
}

interface Order {
  id: string;
  quantity: number;
  meal_name: string;
  seller: string;
  price_cents: number;
  fee_cents: number;
  pickup: string;
  customer_email: string | null;
  created_at: string;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'An unexpected error occurred.';
}

function notify(text: string, error = false) {
  message.textContent = text;
  message.className = error ? 'notice error' : 'notice';
  message.hidden = false;
}

async function api<T>(url: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(url, {
    ...options,
    headers: { 'Content-Type': 'application/json', 'X-Admin-Token': token, ...options.headers }
  });
  const data: unknown = await response.json();
  if (!response.ok) {
    const message = typeof data === 'object' && data !== null && 'error' in data && typeof data.error === 'string'
      ? data.error : 'Request failed.';
    throw new Error(message);
  }
  return data as T;
}

function node<K extends keyof HTMLElementTagNameMap>(tag: K, text: string, className?: string): HTMLElementTagNameMap[K] {
  const result = document.createElement(tag);
  result.textContent = text;
  if (className) result.className = className;
  return result;
}

async function refresh() {
  const [sellers, orders] = await Promise.all([api<Seller[]>('/api/admin/sellers'), api<Order[]>('/api/admin/orders')]);
  login.hidden = true;
  dashboard.hidden = false;
  const list = document.querySelector<HTMLElement>('#sellers')!;
  const select = document.querySelector<HTMLSelectElement>('#meal-form select')!;
  list.replaceChildren();
  select.replaceChildren();
  if (!sellers.length) list.append(node('p', 'No sellers yet.'));
  for (const seller of sellers) {
    const option = node('option', seller.name);
    option.value = String(seller.id);
    select.append(option);
    const row = node('div', '', 'admin-row');
    row.append(node('strong', `${seller.name} — ${seller.approved ? 'approved' : 'not approved'}`),
      node('p', `${seller.kitchen} · Permit: ${seller.permit_reference}`, 'detail'));
    const onboard = node('button', 'Create onboarding link', 'link-button');
    onboard.addEventListener('click', async () => {
      try {
        const result = await api<{ url: string }>(`/api/admin/sellers/${seller.id}/onboarding`, { method: 'POST' });
        const link = node('a', 'Open or copy the single-use onboarding link');
        link.href = result.url;
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        row.append(link);
        notify('Send the onboarding link securely to the seller.');
      } catch (error) { notify(errorMessage(error), true); }
    });
    const approval = node('button', seller.approved ? 'Suspend seller' : 'Approve seller', 'link-button');
    approval.addEventListener('click', async () => {
      try {
        await api(`/api/admin/sellers/${seller.id}`, { method: 'PATCH', body: JSON.stringify({ approved: !seller.approved }) });
        await refresh();
      } catch (error) { notify(errorMessage(error), true); }
    });
    row.append(onboard, approval);
    list.append(row);
  }
  const orderList = document.querySelector<HTMLElement>('#orders')!;
  orderList.replaceChildren();
  if (!orders.length) orderList.append(node('p', 'No paid test orders yet.'));
  for (const order of orders) {
    const row = node('div', '', 'admin-row');
    row.append(node('strong', `${order.quantity} × ${order.meal_name} — ${order.seller}`),
      node('p', `Total: $${(order.price_cents * order.quantity / 100).toFixed(2)} · Platform fee: $${(order.fee_cents / 100).toFixed(2)}`),
      node('p', `Pickup: ${order.pickup} · Customer: ${order.customer_email || 'email not provided'}`),
      node('p', `Order ${order.id} · ${order.created_at} UTC`, 'detail'));
    orderList.append(row);
  }
}

login.addEventListener('submit', async event => {
  event.preventDefault();
  const value = new FormData(login).get('token');
  if (typeof value !== 'string') return notify('Enter an administrator token.', true);
  token = value;
  try {
    await refresh();
    sessionStorage.setItem('cookist-admin-token', token);
    login.reset();
    notify('Admin unlocked.');
  } catch (error) { token = ''; notify(errorMessage(error), true); }
});

sellerForm.addEventListener('submit', async event => {
  event.preventDefault();
  const form = sellerForm;
  try {
    await api('/api/admin/sellers', { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(form))) });
    form.reset();
    await refresh();
    notify('Seller created. Complete Stripe onboarding and independently verify the seller before approval.');
  } catch (error) { notify(errorMessage(error), true); }
});

mealForm.addEventListener('submit', async event => {
  event.preventDefault();
  const form = mealForm;
  const values = Object.fromEntries(new FormData(form));
  const meal = { sellerId: Number(values.sellerId), priceCents: Math.round(Number(values.price) * 100),
    name: values.name, description: values.description, allergens: values.allergens, pickup: values.pickup };
  try {
    await api('/api/admin/meals', { method: 'POST', body: JSON.stringify(meal) });
    form.reset();
    notify('Meal added.');
  } catch (error) { notify(errorMessage(error), true); }
});

document.querySelector<HTMLElement>('#logout')!.addEventListener('click', () => {
  sessionStorage.removeItem('cookist-admin-token');
  token = '';
  dashboard.hidden = true;
  login.hidden = false;
  notify('Admin locked.');
});

if (token) refresh().catch(error => {
  sessionStorage.removeItem('cookist-admin-token');
  token = '';
  notify(errorMessage(error), true);
});
