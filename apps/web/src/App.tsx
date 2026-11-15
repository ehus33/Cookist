import { useEffect, useRef, useState } from 'react';
import { api, errorMessage } from './api';
import { Checkout, Modal, OrderCard, StoreCard, StoreMenu } from './components';
import { changeQuantity, isCartMap, isSession, money } from './domain';
import { useOrders } from './hooks';
import { Icon } from './Icon';
import type { Account, Accounts, Cart, Order, Role, SearchResult, Session, Status, Store } from './types';

const categories = [
  ['All', '🍽️'], ['Pizza', '🍕'], ['Healthy', '🥑'], ['Burgers', '🍔'],
  ['Japanese', '🍣'], ['Mexican', '🌮'], ['Bakery', '🥐'],
];
const rolePlural = { customer: 'customers', merchant: 'merchants', courier: 'couriers' } as const;

function loadLocal() {
  let session: Session | null = null;
  let carts: Record<string, Cart | null> = {};
  let address = '1000 4th Ave, Seattle, WA 98104';
  let error = '';
  try {
    const rawSession = localStorage.getItem('cookist.session');
    const rawCarts = localStorage.getItem('cookist.carts');
    if (rawSession) {
      const data: unknown = JSON.parse(rawSession);
      if (data !== null && !isSession(data)) throw new Error('Saved demo session was invalid. Please select an identity again.');
      session = data;
    }
    if (rawCarts) {
      const data: unknown = JSON.parse(rawCarts);
      if (!isCartMap(data)) throw new Error('Saved bag data was invalid. Please rebuild your bag.');
      carts = data;
    }
    const savedAddress = localStorage.getItem('cookist.address');
    if (savedAddress && savedAddress.length <= 300) address = savedAddress;
  } catch (cause) {
    error = `Local storage: ${errorMessage(cause)}`;
    console.error(error);
  }
  return { session, carts, address, error };
}

export function App() {
  const [local] = useState(loadLocal);
  const [session, setSession] = useState(local.session);
  const [carts, setCarts] = useState(local.carts);
  const [address, setAddress] = useState(local.address);
  const [accounts, setAccounts] = useState<Accounts | null>(null);
  const [catalog, setCatalog] = useState<Store[]>([]);
  const [bootstrapError, setBootstrapError] = useState('');
  const [notice, setNotice] = useState(local.error);
  const [success, setSuccess] = useState('');
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const [view, setView] = useState<'discover' | 'orders'>('discover');
  const [selectedStoreId, setSelectedStoreId] = useState<string | null>(null);
  const [dialog, setDialog] = useState<'cart' | 'address' | null>(null);
  const [checkoutBusy, setCheckoutBusy] = useState(false);
  const [query, setQuery] = useState('');
  const [submittedQuery, setSubmittedQuery] = useState('');
  const [focused, setFocused] = useState(false);
  const [activeSuggestion, setActiveSuggestion] = useState(-1);
  const [cuisine, setCuisine] = useState('All');
  const [sort, setSort] = useState('relevance');
  const [lowFee, setLowFee] = useState(false);
  const [openOnly, setOpenOnly] = useState(false);
  const [result, setResult] = useState<SearchResult>({ query: '', stores: [], suggestions: [] });
  const [searchError, setSearchError] = useState('');
  const [searching, setSearching] = useState(true);
  const [addressDraft, setAddressDraft] = useState(address);
  const searchBox = useRef<HTMLInputElement>(null);
  const role: Role = session?.role ?? 'customer';
  const owner = session?.actorId ?? 'guest';
  const cart = carts[owner] ?? null;
  const cartStore = catalog.find(store => store.id === cart?.storeId);
  const selectedStore = catalog.find(store => store.id === selectedStoreId);
  const bagCount = Object.values(cart?.items ?? {}).reduce((a, b) => a + b, 0);
  const actorList: Account[] = accounts?.[rolePlural[role]] ?? [];
  const actor = actorList.find(item => item.id === session?.actorId);
  const merchantStore = catalog.find(store => store.id === actor?.storeId);
  const { orders, error: ordersError, loading: ordersLoading } = useOrders(session, revision);
  const isOrdersView = role !== 'customer' || view === 'orders';
  const suggestionsVisible = focused && query.trim().length > 0 && result.query === query.trim() && result.suggestions.length > 0;

  useEffect(() => {
    try {
      localStorage.setItem('cookist.session', JSON.stringify(session));
      localStorage.setItem('cookist.carts', JSON.stringify(carts));
      localStorage.setItem('cookist.address', address);
    } catch (error) {
      setNotice(`Your changes cannot be saved on this browser: ${errorMessage(error)}`);
    }
  }, [session, carts, address]);

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      api<Accounts>('/api/demo-accounts', null, { signal: controller.signal }),
      api<Store[]>('/api/stores', null, { signal: controller.signal }),
    ]).then(([accounts, stores]) => {
      setAccounts(accounts); setCatalog(stores); setBootstrapError('');
    }).catch(error => { if (!controller.signal.aborted) setBootstrapError(errorMessage(error)); });
    return () => controller.abort();
  }, [revision]);

  useEffect(() => {
    const controller = new AbortController();
    setSearching(true);
    setActiveSuggestion(-1);
    const timer = setTimeout(() => {
      const params = new URLSearchParams({ q: query.trim(), sort, openOnly: String(openOnly) });
      if (cuisine !== 'All') params.set('cuisine', cuisine);
      if (lowFee) params.set('maxFeeCents', '199');
      api<SearchResult>(`/api/search?${params}`, null, { signal: controller.signal })
        .then(data => { if (!controller.signal.aborted) { setResult(data); setSearchError(''); } })
        .catch(error => { if (!controller.signal.aborted) { setSearchError(errorMessage(error)); setResult({ query, stores: [], suggestions: [] }); } })
        .finally(() => { if (!controller.signal.aborted) setSearching(false); });
    }, 250);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [query, cuisine, sort, lowFee, openOnly, revision]);

  function writeCart(value: Cart | null) { setCarts(current => ({ ...current, [owner]: value })); }
  function add(storeId: string, itemId: string, delta: number) {
    try { writeCart(changeQuantity(cart, storeId, itemId, delta)); setNotice(''); }
    catch (error) { setNotice(errorMessage(error)); }
  }
  async function login(nextRole: Role, actorId?: string) {
    const id = actorId ?? accounts?.[rolePlural[nextRole]][0]?.id;
    if (!id || busy) return;
    setBusy(true); setNotice('');
    try {
      const next = await api<Session>('/api/sessions', null, { method: 'POST', body: { role: nextRole, actorId: id } });
      // Guest food can follow the first demo customer; never share another customer's bag.
      if (!session && nextRole === 'customer' && cart) setCarts(current => ({ ...current, [id]: current[id] ?? cart, guest: null }));
      setSession(next); setView('discover'); setSelectedStoreId(null); setRevision(value => value + 1);
      setSuccess(`You're trying Cookist as ${accounts?.[rolePlural[nextRole]].find(account => account.id === id)?.name ?? nextRole}.`);
    } catch (error) { setNotice(errorMessage(error)); }
    finally { setBusy(false); }
  }
  async function mutate(path: string, body: object, message: string, method = 'POST') {
    if (!session || busy) return;
    setBusy(true); setNotice('');
    try {
      await api(path, session, { method, body });
      setRevision(value => value + 1); setSuccess(message);
    } catch (error) { setNotice(errorMessage(error)); }
    finally { setBusy(false); }
  }
  function selectSuggestion(index: number) {
    const suggestion = result.suggestions[index];
    if (!suggestion) return;
    if (suggestion.type === 'store' && suggestion.storeId) setSelectedStoreId(suggestion.storeId);
    else { setQuery(suggestion.text); setSubmittedQuery(suggestion.text); setSelectedStoreId(null); }
    setFocused(false); setView('discover');
  }
  function discover() { setView('discover'); setSelectedStoreId(null); }
  function orderStatus(order: Order, status: Status) {
    if (status === 'cancelled' && !confirm('Cancel this order and refund the simulated payment?')) return;
    void mutate(`/api/orders/${order.id}/status`, { status }, 'Order updated.');
  }

  return <div className="app-shell">
    <aside className="sidebar">
      <a className="brand-mark" href="/" aria-label="Cookist home">c<span /></a>
      <nav aria-label="Main navigation">
        <button className={!isOrdersView ? 'nav-item active' : 'nav-item'} onClick={discover} disabled={role !== 'customer'} aria-label="Discover restaurants"><Icon name="home" /><span>Discover</span></button>
        <button className={isOrdersView ? 'nav-item active' : 'nav-item'} onClick={() => { setView('orders'); setSelectedStoreId(null); }} aria-label={role === 'courier' ? 'Deliveries' : 'Orders'}><Icon name={role === 'courier' ? 'bike' : 'receipt'} /><span>{role === 'courier' ? 'Deliveries' : 'Orders'}</span>{orders.filter(order => !['cancelled', 'delivered'].includes(order.status)).length > 0 && <i />}</button>
      </nav>
      <div className="sidebar-bottom"><Icon name="leaf" size={24} /><span>Good food.<br />Good neighbors.</span><small>EST. 2026</small></div>
    </aside>
    <div className="workspace">
      <div className="demo-bar"><span><i /> THE COOKIST LOCAL DEMO <b>·</b> No real payments or deliveries</span><div className="role-tabs" aria-label="Demo workspace">{(['customer', 'merchant', 'courier'] as const).map(item =>
        <button key={item} className={role === item ? 'selected' : ''} disabled={busy || !accounts} onClick={() => void login(item)}>{item}</button>)}</div></div>
      <header className="header">
        <button className="wordmark" onClick={discover}>cookist<span>®</span></button>
        <div className="header-divider" />
        <button className="delivery-location" onClick={() => { setAddressDraft(address); setDialog('address'); }}><span className="location-symbol"><Icon name="pin" size={19} /></span><span><small>DELIVERING A LITTLE HAPPINESS TO</small><strong>{address || 'Set your delivery address'} <Icon name="chevron" size={14} /></strong></span></button>
        <div className="header-actions">
          <label className="account-select"><span className="avatar">{actor?.name.charAt(0) ?? 'G'}</span><select aria-label="Demo identity" disabled={busy || !accounts} value={session?.actorId ?? ''} onChange={event => void login(role, event.target.value)}>
            {!session && <option value="">Browse as guest</option>}{actorList.map(account => <option value={account.id} key={account.id}>{account.name}</option>)}
          </select></label>
          {session && <button className="icon-button logout" aria-label="End demo session" title="End demo session" disabled={busy} onClick={() => { setSession(null); discover(); setSuccess('Demo session ended.'); }}><Icon name="logout" size={18} /></button>}
          <button className="bag-button" onClick={() => setDialog('cart')} aria-label={`Open bag, ${bagCount} items`}><Icon name="bag" size={18} /><span>Bag</span><b>{bagCount}</b></button>
        </div>
      </header>
      <main id="main-content">
        {notice && <div className="notice error global-notice" role="alert"><span>{notice}</span><button className="icon-button" aria-label="Dismiss error" onClick={() => setNotice('')}><Icon name="close" size={16} /></button></div>}
        {success && <div className="notice success global-notice" role="status"><span><Icon name="check" size={15} /> {success}</span><button className="icon-button" aria-label="Dismiss notification" onClick={() => setSuccess('')}><Icon name="close" size={16} /></button></div>}
        {bootstrapError && <div className="notice error" role="alert"><span>Can't reach Cookist: {bootstrapError}</span><button className="secondary" onClick={() => setRevision(value => value + 1)}>Retry connection</button></div>}
        {role === 'customer' && view === 'discover' && (selectedStore ? <StoreMenu store={selectedStore} cart={cart} onBack={() => setSelectedStoreId(null)} onAdd={(id, delta) => add(selectedStore.id, id, delta)} onBag={() => setDialog('cart')} /> : <>
          <section className="hero">
            <div className="hero-copy"><span className="eyebrow"><span className="tiny-sun">✳</span> YOUR NEIGHBORHOOD, DELIVERED</span><h1>Good food.<br /><em>Great neighborhood.</em></h1><p>The local spots you love. The next ones you will.<br />A little closer, one delicious delivery at a time.</p><button className="hero-link" onClick={() => searchBox.current?.focus()}>What are you craving? <span><Icon name="arrow" size={17} /></span></button></div>
            <div className="hero-art" aria-hidden="true"><div className="hero-orbit orbit-one" /><div className="hero-orbit orbit-two" /><span className="hero-spark spark-one">✳</span><span className="hero-spark spark-two">✦</span><div className="hero-plate"><span>🥗</span></div><span className="hero-tomato">🍅</span><span className="hero-leaf">🌿</span><div className="hero-sticker"><Icon name="leaf" size={18} /><span>LOCAL TASTES<br /><b>better.</b></span></div><div className="hero-note">made around the corner ↗</div></div>
          </section>
          <section className="discovery" aria-label="Restaurant discovery">
            <div className="search-row">
              <div className="search-container"><form className={`search-input ${focused ? 'focused' : ''}`} onSubmit={event => { event.preventDefault(); if (suggestionsVisible && activeSuggestion >= 0) selectSuggestion(activeSuggestion); else { setSubmittedQuery(query.trim()); setFocused(false); } }}>
                <Icon name="search" size={21} /><input ref={searchBox} role="combobox" aria-label="Search restaurants or dishes" aria-autocomplete="list" aria-expanded={suggestionsVisible} aria-controls="search-suggestions" aria-activedescendant={suggestionsVisible && activeSuggestion >= 0 ? `suggestion-${activeSuggestion}` : undefined}
                  value={query} onChange={event => { setQuery(event.target.value); setFocused(true); }} onFocus={() => setFocused(true)}
                  onBlur={event => { if (!event.currentTarget.form?.parentElement?.contains(event.relatedTarget)) setFocused(false); }}
                  onKeyDown={event => { if (event.key === 'Escape') setFocused(false); if (suggestionsVisible && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) { event.preventDefault(); setActiveSuggestion(index => (index + (event.key === 'ArrowDown' ? 1 : -1) + result.suggestions.length) % result.suggestions.length); } }}
                  placeholder="A dish, a craving, a neighborhood favorite…" maxLength={100} />
                {query && <button type="button" className="icon-button" aria-label="Clear search" onClick={() => { setQuery(''); setSubmittedQuery(''); searchBox.current?.focus(); }}><Icon name="close" size={16} /></button>}<kbd>↵</kbd>
              </form>
                {suggestionsVisible && <div id="search-suggestions" role="listbox" aria-label="Search suggestions" className="suggestions">{result.suggestions.map((suggestion, index) => <button type="button" role="option" aria-selected={activeSuggestion === index} id={`suggestion-${index}`} key={`${suggestion.type}-${suggestion.text}`} onMouseDown={event => event.preventDefault()} onClick={() => selectSuggestion(index)}><span className="suggestion-icon"><Icon name={suggestion.type === 'store' ? 'shop' : 'search'} size={17} /></span><span>{suggestion.text}<small>{suggestion.type === 'store' ? 'Local kitchen' : 'Search for this craving'}</small></span><Icon name="arrow" size={15} /></button>)}</div>}
              </div>
              <span className="delivery-promise"><Icon name="bike" size={20} /><span>Small distance.<br /><strong>Big flavor.</strong></span></span>
            </div>
            <div className="categories" aria-label="Cuisine filters">{categories.map(([name, emoji]) => <button key={name} onClick={() => setCuisine(name)} className={cuisine === name ? 'category selected' : 'category'} aria-pressed={cuisine === name}><span aria-hidden="true">{emoji}</span><strong>{name === 'All' ? 'Everything' : name}</strong>{cuisine === name && <i />}</button>)}</div>
            <div className="section-heading"><div><span className="eyebrow">{query.trim() ? 'LET’S FIND YOUR CRAVING' : 'PULL UP A CHAIR'}</span><h2>{query.trim() ? `Good things with “${submittedQuery === query.trim() ? submittedQuery : query.trim()}”` : cuisine === 'All' ? 'Your next local favorite.' : `${cuisine}, made nearby.`}</h2><p>{searching ? 'Looking around the neighborhood…' : `${result.stores.length} local ${result.stores.length === 1 ? 'kitchen' : 'kitchens'} worth staying in for.`}</p></div><span className="local-tag"><Icon name="pin" size={14} /> Seattle neighborhood demo</span></div>
            <div className="filters"><button className={lowFee ? 'filter-chip selected' : 'filter-chip'} aria-pressed={lowFee} onClick={() => setLowFee(value => !value)}><Icon name="sliders" size={15} /> Delivery under $2 {lowFee && <Icon name="check" size={13} />}</button><button className={openOnly ? 'filter-chip selected' : 'filter-chip'} aria-pressed={openOnly} onClick={() => setOpenOnly(value => !value)}>Open now {openOnly && <Icon name="check" size={13} />}</button>
              <label className="sort-select">Sort by <select aria-label="Sort restaurants" value={sort} onChange={event => setSort(event.target.value)}><option value="relevance">Recommended</option><option value="rating">Top rated</option><option value="eta">Fastest delivery</option><option value="fee">Delivery fee</option></select></label></div>
            {searchError ? <div className="notice error" role="alert">{searchError}<button className="secondary" onClick={() => setRevision(value => value + 1)}>Try again</button></div>
              : <><div className={`store-grid ${searching ? 'is-searching' : ''}`} aria-busy={searching}>{result.stores.map((store, index) => <StoreCard key={store.id} store={store} index={index} onOpen={() => { setSelectedStoreId(store.id); setFocused(false); }} />)}</div>
                {!searching && result.stores.length === 0 && <div className="empty-state"><span className="empty-emoji" aria-hidden="true">🔎</span><h3>No kitchens found. A new craving?</h3><p>Try pizza, a dish like miso, or clear a filter.</p><button className="secondary" onClick={() => { setQuery(''); setSubmittedQuery(''); setCuisine('All'); setLowFee(false); setOpenOnly(false); }}>Reset search</button></div>}</>}
          </section>
          <section className="neighborhood-note"><span className="note-icon"><Icon name="leaf" size={27} /></span><div><h3>A good neighborhood starts at the table.</h3><p>Independent kitchens. Familiar faces. A fresh reason to eat local.</p></div><span className="note-handwriting">here’s to good taste.</span></section>
        </>)}
        {isOrdersView && <>
          <div className="page-heading"><div><span className="eyebrow">{role === 'merchant' ? 'THE KITCHEN WORKSPACE' : role === 'courier' ? 'GOOD FOOD, ON THE MOVE' : 'YOUR LITTLE MOMENTS OF DELICIOUS'}</span><h1>{role === 'merchant' ? 'Hello, neighborhood chef.' : role === 'courier' ? 'Your next delivery awaits.' : 'Your orders.'}</h1><p>{role === 'merchant' ? 'Keep your menu fresh and your orders moving.' : role === 'courier' ? 'Claim a ready order, pick it up, and bring it home.' : 'From the first sizzle to your doorstep.'}</p></div><button className="secondary" disabled={busy || ordersLoading} onClick={() => setRevision(value => value + 1)}>{ordersLoading ? 'Refreshing…' : 'Refresh orders'}</button></div>
          {!session && <div className="empty-state"><Icon name="receipt" size={44} /><h3>Your food story starts here.</h3><p>Choose a demo identity to order and follow your delivery.</p><button className="primary" disabled={!accounts || busy} onClick={() => void login('customer')}>Try as Alex <Icon name="arrow" size={16} /></button></div>}
          {role === 'merchant' && merchantStore && <>
            <div className="merchant-overview"><div className="merchant-store"><span style={{ background: merchantStore.color }}>{merchantStore.emoji}</span><div><small>YOUR KITCHEN</small><h3>{merchantStore.name}</h3></div><button className={merchantStore.isOpen ? 'availability open' : 'availability'} disabled={busy} onClick={() => void mutate(`/api/stores/${merchantStore.id}/availability`, { isOpen: !merchantStore.isOpen }, 'Kitchen availability updated.', 'PATCH')}><i />{merchantStore.isOpen ? 'Open for orders' : 'Kitchen closed'}</button></div>
              <div className="merchant-stats"><div><span>Active orders</span><strong>{orders.filter(order => !['delivered', 'cancelled'].includes(order.status)).length}</strong></div><div><span>Demo sales, all time</span><strong>{money(orders.filter(order => order.payment.status !== 'refunded').reduce((total, order) => total + order.totals.subtotalCents, 0))}</strong></div><div><span>Neighborhood rating</span><strong>{merchantStore.rating} <small>/ 5</small></strong></div></div></div>
            <details className="menu-management"><summary><Icon name="sliders" size={17} /> Manage menu availability <span>{merchantStore.menu.filter(item => item.available).length} / {merchantStore.menu.length} available</span></summary><div>{merchantStore.menu.map(item => <div className="availability-row" key={item.id}><span>{item.emoji} {item.name} <small>{money(item.priceCents)}</small></span><button className={item.available ? 'toggle enabled' : 'toggle'} aria-label={`${item.name} availability`} aria-pressed={item.available} disabled={busy} onClick={() => void mutate(`/api/menu-items/${item.id}`, { available: !item.available }, 'Menu availability updated.', 'PATCH')}><span />{item.available ? 'Available' : 'Sold out'}</button></div>)}</div></details>
          </>}
          {session && <div className="live-heading"><h2>{role === 'courier' ? 'Available & assigned deliveries' : role === 'merchant' ? 'Incoming & recent orders' : 'Fresh from your kitchens'}</h2><span><i /> Updates every 4 seconds</span></div>}
          {ordersError && <div className="notice error" role="alert">{ordersError} <button className="text-button" onClick={() => { setSession(null); discover(); }}>End session & sign in again</button></div>}
          {session && !ordersError && !ordersLoading && orders.length === 0 && <div className="empty-state"><span className="empty-emoji" aria-hidden="true">{role === 'courier' ? '🚲' : '🍽️'}</span><h3>{role === 'courier' ? 'The next delicious delivery is on its way.' : 'Nothing on the table just yet.'}</h3><p>{role === 'courier' ? 'Ask a kitchen to mark an order ready, then claim it here.' : role === 'merchant' ? 'Customer orders will appear here as soon as they are placed.' : 'Find a local favorite and make it your first order.'}</p>{role === 'customer' && <button className="primary" onClick={discover}>Find something delicious <Icon name="arrow" size={16} /></button>}</div>}
          <div className="orders-list">{session && orders.map(order => <OrderCard key={order.id} order={order} role={role} actorId={session.actorId} store={catalog.find(store => store.id === order.storeId)} busy={busy} onStatus={orderStatus}
            onClaim={order => void mutate(`/api/orders/${order.id}/claim`, {}, 'Delivery claimed. The neighborhood is counting on you.')}
            onLocation={(order, location) => void mutate(`/api/orders/${order.id}/location`, location, 'Simulated location shared.')} />)}</div>
        </>}
      </main>
      <footer><span className="footer-wordmark">cookist.</span><span>A little local goes a long way.</span><small>Original local demo · Seeded sample kitchens · Simulated payments & tracking</small></footer>
    </div>
    {dialog === 'cart' && <Modal title="Your bag of good things" locked={checkoutBusy} onClose={() => setDialog(null)}><Checkout cart={cart} store={cartStore} session={session} address={address} onAddress={setAddress} onQuantity={(id, delta) => { if (cart) add(cart.storeId, id, delta); }} onClear={() => writeCart(null)} onLogin={() => void login('customer')} onBusy={setCheckoutBusy}
      onSuccess={order => { writeCart(null); setDialog(null); setView('orders'); setSelectedStoreId(null); setRevision(value => value + 1); setSuccess(`Order #${order.id.slice(-8).toUpperCase()} placed. Your local kitchen is on it!`); }} /></Modal>}
    {dialog === 'address' && <Modal title="Where should the good food go?" onClose={() => setDialog(null)}><form className="address-form" onSubmit={event => { event.preventDefault(); setAddress(addressDraft.trim()); setDialog(null); }}><p>This demo uses a seeded Seattle catalog. Changing the address does not recalculate distance or availability.</p><label className="field">Delivery address<textarea minLength={8} maxLength={300} required value={addressDraft} onChange={event => setAddressDraft(event.target.value)} /></label><button className="primary full">Save delivery address <Icon name="arrow" size={17} /></button></form></Modal>}
  </div>;
}
