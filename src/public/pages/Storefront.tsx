import { Header } from '../components/Header.js';
import { MealCard } from '../components/MealCard.js';
import { useStorefront } from '../hooks/useStorefront.js';

export function Storefront() {
  const { meals, loadError, busy, notice, noticeError, checkoutMeal } = useStorefront();

  return <>
    <Header><span className="header-note">Made in Seattle · Pickup only</span></Header>
    <main>
      <section className="hero wrap"><div>
        <p className="eyebrow">A LITTLE MORE LOCAL</p>
        <h1>Good food.<br /><em>Good neighbors.</em></h1>
        <p className="intro">Discover dishes from independent cooks making food in approved shared kitchens around Seattle.</p>
        <a className="button" href="#menu">Explore the menu <span aria-hidden="true">↗</span></a>
      </div><div className="hero-art" aria-hidden="true">
        <div className="plate"><div className="plate-inner">made<br />with care<span>✳</span></div></div>
        <span className="stamp">LOCAL<br />FLAVOR</span>
      </div></section>
      <section className="test-banner"><div className="wrap">PILOT PREVIEW <span>·</span> Test payments only. No real orders or food are being sold.</div></section>
      <section className="menu wrap" id="menu">
        <div className="section-heading"><div><p className="eyebrow">FROM OUR COMMUNITY</p><h2>On the menu</h2></div>
          <p>Find something delicious, then pick it up at the listed location.</p></div>
        {notice && <p role="status" className={noticeError ? 'notice error' : 'notice'}>{notice}</p>}
        <div className="meal-grid" aria-live="polite">
          {loadError ? <p className="error">{loadError}</p> : !meals ? <p>Loading meals…</p> : meals.length === 0
            ? <p className="empty">No meals are listed yet. Our pilot menu is coming soon.</p>
            : meals.map(meal => <MealCard key={meal.id} meal={meal} disabled={busy !== null} onCheckout={checkoutMeal} />)}
        </div>
      </section>
      <section className="how wrap"><p className="eyebrow">SIMPLE AS THAT</p><h2>Made locally. Picked up by you.</h2>
        <div className="steps">
          <div><span>01</span><h3>Find your dish</h3><p>Browse meals and check ingredients, allergens, and pickup details.</p></div>
          <div><span>02</span><h3>Place an order</h3><p>Try a secure test checkout. This pilot does not take real payments.</p></div>
          <div><span>03</span><h3>Pick up locally</h3><p>For a future live launch, meet your cook at the approved pickup location.</p></div>
        </div>
      </section>
    </main>
    <footer className="wrap footer"><a className="brand" href="/">cookist<span>.</span></a><p>A test-mode Seattle food marketplace.</p><a href="/admin.html">Admin</a></footer>
  </>;
}
