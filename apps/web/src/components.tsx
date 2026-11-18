import { useEffect, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { api, errorMessage } from './api';
import { Icon } from './Icon';
import { cartTotals, journey, money, nextStatuses, statusLabels } from './domain';
import { TrackingMap } from './TrackingMap';
import { CourierLocationControls } from './CourierLocationControls';
import type { Cart, Order, Role, Session, Store } from './types';

export function Modal({ title, children, onClose, locked = false }: { title: string; children: ReactNode; onClose: () => void; locked?: boolean }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => { dialog?.close(); };
  }, []);
  return <dialog ref={ref} className="modal" aria-label={title} onCancel={event => { if (locked) event.preventDefault(); else onClose(); }}>
    <div className="modal-heading"><h2>{title}</h2><button className="icon-button" aria-label="Close dialog" disabled={locked} onClick={onClose}><Icon name="close" /></button></div>
    {children}
  </dialog>;
}

export function StoreCard({ store, onOpen, index }: { store: Store; onOpen: () => void; index: number }) {
  return <button className="store-card" onClick={onOpen} style={{ animationDelay: `${index * 45}ms` }}>
    <div className={`store-art art-${store.id}`} style={{ backgroundColor: store.color }}>
      <span className="art-ring" /><span className="food-emoji" aria-hidden="true">{store.emoji}</span>
      <span className="art-garnish garnish-one" /><span className="art-garnish garnish-two" />
      {index === 0 && <span className="card-ribbon"><Icon name="leaf" size={12} /> Neighborhood favorite</span>}
      {!store.isOpen && <span className="closed-badge">Currently closed</span>}
      <span className="art-label">{store.cuisine.toUpperCase()} / MADE WITH LOVE</span>
    </div>
    <div className="store-card-body">
      <div className="store-title"><h3>{store.name}</h3><span className="rating"><Icon name="star" size={13} />{store.rating.toFixed(1)} <small>({store.reviewCount})</small></span></div>
      <p>{store.cuisine} <span>·</span> {store.distanceMiles} mi away</p>
      <div className="store-meta"><span><Icon name="clock" size={13} /> {store.etaMinutes}–{store.etaMinutes + 10} min</span><span>{money(store.deliveryFeeCents)} delivery</span></div>
    </div>
  </button>;
}

export function StoreMenu({ store, cart, onBack, onAdd, onBag }: {
  store: Store; cart: Cart | null; onBack: () => void; onAdd: (itemId: string, delta: number) => void; onBag: () => void;
}) {
  const groups = [...new Set(store.menu.map(item => item.category))];
  return <>
    <button className="text-button back-button" onClick={onBack}><Icon name="back" size={17} /> Back to your neighborhood</button>
    <div className="restaurant-banner" style={{ backgroundColor: store.color }}>
      <div><span className="eyebrow">{store.cuisine} / LOCAL & LOVELY</span><h1>{store.name}</h1><p>{store.description}</p>
        <div className="restaurant-badges"><span><Icon name="star" size={14} /> {store.rating} ({store.reviewCount} reviews)</span><span><Icon name="clock" size={14} /> {store.etaMinutes}–{store.etaMinutes + 10} min</span><span>{money(store.deliveryFeeCents)} delivery</span></div>
      </div><span className="restaurant-emoji" aria-hidden="true">{store.emoji}</span>
    </div>
    {!store.isOpen && <div role="status" className="notice">This kitchen is closed. Browse the menu and come back when it reopens.</div>}
    <div className="menu-heading"><div><span className="eyebrow">FRESH FROM THE KITCHEN</span><h2>Find your new favorite.</h2></div>
      {cart?.storeId === store.id && <button className="primary" onClick={onBag}><Icon name="bag" size={17} /> View bag</button>}</div>
    {groups.map(category => <section key={category} className="menu-group"><h3>{category}</h3><div className="menu-grid">
      {store.menu.filter(item => item.category === category).map(item => {
        const quantity = cart?.storeId === store.id ? cart.items[item.id] ?? 0 : 0;
        return <article className={`menu-item ${!item.available ? 'unavailable' : ''}`} key={item.id}>
          <div className="menu-copy"><h4>{item.name}</h4><p>{item.description}</p><strong>{money(item.priceCents)}</strong>
            <div className="menu-controls">{quantity > 0 && <><button className="round-button" aria-label={`Remove one ${item.name}`} onClick={() => onAdd(item.id, -1)}><Icon name="minus" size={15} /></button><span aria-label={`${quantity} in bag`}>{quantity}</span></>}
              <button className="round-button add-button" disabled={!item.available || !store.isOpen || quantity >= 20} aria-label={`Add ${item.name}`} onClick={() => onAdd(item.id, 1)}><Icon name="plus" size={17} /></button>
              {!item.available && <small>Sold out</small>}
            </div>
          </div><div className="menu-item-art" style={{ backgroundColor: store.color }} aria-hidden="true">{item.emoji}</div>
        </article>;
      })}
    </div></section>)}
  </>;
}

export function Checkout({ cart, store, session, address, onAddress, onQuantity, onClear, onLogin, onSuccess, onBusy }: {
  cart: Cart | null; store: Store | undefined; session: Session | null; address: string;
  onAddress: (value: string) => void; onQuantity: (itemId: string, delta: number) => void;
  onClear: () => void; onLogin: () => void; onSuccess: (order: Order) => void; onBusy?: (busy: boolean) => void;
}) {
  const [paymentMethod, setPaymentMethod] = useState('demo_card');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const totals = cartTotals(cart, store);
  const missingItems = cart && store && Object.keys(cart.items).some(id => !store.menu.some(item => item.id === id));
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!session || !cart || !store || busy) return;
    const body = {
      storeId: store.id, deliveryAddress: address.trim(), paymentMethod,
      items: Object.entries(cart.items).map(([menuItemId, quantity]) => ({ menuItemId, quantity })),
    };
    setBusy(true);
    onBusy?.(true);
    setError('');
    try {
      const fingerprint = JSON.stringify(body);
      const storageKey = `cookist.checkout.${session.actorId}`;
      const saved = sessionStorage.getItem(storageKey);
      let key: string = crypto.randomUUID();
      if (saved) {
        const previous: unknown = JSON.parse(saved);
        if (typeof previous !== 'object' || previous === null
          || !('fingerprint' in previous) || typeof previous.fingerprint !== 'string'
          || !('key' in previous) || typeof previous.key !== 'string') {
          throw new Error('Saved checkout attempt is invalid. Check your orders before clearing this browser’s session storage.');
        }
        if (previous.fingerprint === fingerprint) key = previous.key;
      }
      sessionStorage.setItem(storageKey, JSON.stringify({ fingerprint, key }));
      const order = await api<Order>('/api/orders', session, {
        method: 'POST', body: { ...body, idempotencyKey: key },
      });
      sessionStorage.removeItem(storageKey);
      onSuccess(order);
    } catch (error) {
      setError(errorMessage(error));
    } finally { setBusy(false); onBusy?.(false); }
  }
  if (!cart) return <div className="empty-state"><span className="empty-emoji" aria-hidden="true">🛍️</span><h3>Your next favorite is out there.</h3><p>Your bag is empty. Add a little something from a local kitchen.</p></div>;
  if (!store) return <div role="alert" className="notice error">This kitchen is unavailable. <button className="text-button" onClick={onClear}>Clear bag</button></div>;
  return <form onSubmit={submit} className="checkout-form">
    <div className="bag-store"><span style={{ backgroundColor: store.color }}>{store.emoji}</span><div><small>FRESH FROM</small><h3>{store.name}</h3></div><button type="button" className="text-button" disabled={busy} onClick={onClear}>Clear bag</button></div>
    <div className="bag-items">{store.menu.filter(item => cart.items[item.id]).map(item =>
      <div className="bag-item" key={item.id}><div><strong>{item.name}</strong><small>{money(item.priceCents)} each {!item.available ? ' · Sold out' : ''}</small></div>
        <div className="quantity"><button type="button" aria-label={`Remove one ${item.name}`} disabled={busy} onClick={() => onQuantity(item.id, -1)}><Icon name="minus" size={13} /></button><span>{cart.items[item.id]}</span><button type="button" aria-label={`Add one ${item.name}`} disabled={busy || cart.items[item.id] >= 20} onClick={() => onQuantity(item.id, 1)}><Icon name="plus" size={13} /></button></div>
        <strong>{money(item.priceCents * cart.items[item.id])}</strong>
      </div>)}</div>
    <label className="field">Deliver to<textarea required minLength={8} maxLength={300} value={address} disabled={busy} onChange={event => onAddress(event.target.value)} placeholder="Street address, city, and ZIP" /></label>
    <p className="field-hint">Local demo: addresses are saved with orders. No geocoding or delivery-area verification.</p>
    <label className="field">Simulated payment<select value={paymentMethod} disabled={busy} onChange={event => setPaymentMethod(event.target.value)}><option value="demo_card">Demo card · successful payment</option><option value="demo_decline">Demo card · declined payment</option></select></label>
    <div className="demo-notice"><Icon name="check" size={16} /><span>No real money. No card details. This is a local simulation.</span></div>
    <div className="totals">
      <div><span>Subtotal</span><span>{money(totals.subtotalCents)}</span></div>
      <div><span>Delivery fee</span><span>{money(totals.deliveryFeeCents)}</span></div>
      <div><span>Service fee</span><span>{money(totals.serviceFeeCents)}</span></div>
      <div><span>Estimated tax (10%)</span><span>{money(totals.taxCents)}</span></div>
      <div className="total"><strong>Total</strong><strong>{money(totals.totalCents)}</strong></div>
    </div>
    <p className="field-hint">The server verifies availability and calculates the final total at checkout.</p>
    {error && <p className="notice error" role="alert">{error} You can retry safely; duplicate submissions reuse the same payment attempt.</p>}
    {missingItems && <p className="notice error" role="alert">Some items no longer exist. Clear the bag and choose again.</p>}
    {!session ? <button className="primary full" type="button" onClick={onLogin}>Continue as demo customer <Icon name="arrow" size={17} /></button>
      : session.role !== 'customer' ? <p className="notice">Switch to the customer workspace to check out.</p>
        : <button className="primary full" disabled={busy || !store.isOpen || !!missingItems || store.menu.some(item => cart.items[item.id] && !item.available)}>
          {busy ? 'Placing your order…' : `Place demo order · ${money(totals.totalCents)}`}<Icon name="arrow" size={17} /></button>}
  </form>;
}

const actionLabels: Partial<Record<Order['status'], string>> = {
  accepted: 'Accept order', preparing: 'Start cooking', ready: 'Mark ready',
  picked_up: 'Confirm pickup', delivered: 'Complete delivery', cancelled: 'Cancel & refund',
};

export function Tracking({ order, store }: { order: Order; store?: Store }) {
  return <div className="tracking">
    <TrackingMap store={store} location={order.location} />
    <div className="tracking-caption"><span><Icon name="bike" size={18} /> {order.courierName ?? 'Finding a neighborhood courier'}</span><small>{order.location ? `${order.location.lat.toFixed(4)}, ${order.location.lng.toFixed(4)} · ${order.location.source === 'live' ? 'live location' : 'manually simulated'}` : 'No courier location shared'}</small></div>
    <div className="map-legend">K: demo kitchen · C: {order.location?.source === 'live' ? 'live' : 'simulated'} courier. The delivery address is not mapped.</div>
  </div>;
}

export function OrderCard({ order, role, actorId, store, session, busy, onStatus, onClaim, onLocation }: {
  order: Order; role: Role; actorId: string; store?: Store; session: Session; busy: boolean;
  onStatus: (order: Order, status: Order['status']) => void;
  onClaim: (order: Order) => void; onLocation: (order: Order, location: { lat: number; lng: number }) => void;
}) {
  const active = !['delivered', 'cancelled'].includes(order.status);
  const claimed = order.courierId === actorId;
  const currentStep = journey.indexOf(order.status);
  return <article className="order-card">
    <div className="order-header"><div><span className="eyebrow">ORDER #{order.id.slice(-8).toUpperCase()}</span><h3>{order.storeName}</h3><small>{new Date(order.createdAt).toLocaleString()}</small></div><span className={`status-pill status-${order.status}`}>{statusLabels[order.status]}</span></div>
    <div className="order-content">
      <div>
        <div className="order-customer"><Icon name="pin" size={17} /><div><strong>{order.customerName}</strong><p>{order.deliveryAddress}</p></div></div>
        <ul className="order-lines">{order.items.map(item => <li key={item.menuItemId}><span><b>{item.quantity}×</b> {item.name}</span><span>{money(item.unitPriceCents * item.quantity)}</span></li>)}</ul>
        <div className="order-total"><span>{order.payment.status === 'refunded' ? 'Demo payment refunded' : 'Demo payment captured'}</span><strong>{money(order.totals.totalCents)}</strong></div>
        {active && <ol className="journey" aria-label="Order progress">{journey.map((status, index) => <li key={status} className={index <= currentStep ? 'reached' : ''} aria-current={index === currentStep ? 'step' : undefined}><span>{index < currentStep ? <Icon name="check" size={11} /> : index + 1}</span><small>{status === 'pending' ? 'Placed' : status === 'picked_up' ? 'On the way' : status === 'preparing' ? 'Cooking' : status === 'accepted' ? 'Accepted' : status === 'ready' ? 'Ready' : 'Delivered'}</small></li>)}</ol>}
        <div className="order-actions">
          {role === 'courier' && order.status === 'ready' && !order.courierId && <button className="primary" disabled={busy} onClick={() => onClaim(order)}>Claim delivery <Icon name="arrow" size={16} /></button>}
          {nextStatuses(role, order.status, claimed).map(status => <button key={status} disabled={busy} className={status === 'cancelled' ? 'text-button danger' : 'primary'} onClick={() => onStatus(order, status)}>{actionLabels[status]}</button>)}
        </div>
        {role === 'courier' && claimed && active && <CourierLocationControls session={session} order={order} store={store} busy={busy} onLocation={onLocation} />}
        <details className="event-log"><summary>Order activity ({order.events.length})</summary><ol>{order.events.map((event, index) => <li key={`${event.at}-${index}`}><time>{new Date(event.at).toLocaleTimeString()}</time><span>{event.note}</span></li>)}</ol></details>
      </div>
      {(role === 'customer' || (role === 'courier' && claimed)) && order.status !== 'cancelled' && <Tracking order={order} store={store} />}
    </div>
  </article>;
}
