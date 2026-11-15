import { Header } from '../components/Header.js';
import { LoginForm, MealForm, SellerForm } from '../components/AdminForms.js';
import { OrderList } from '../components/OrderList.js';
import { SellerList } from '../components/SellerList.js';
import { useAdmin } from '../hooks/useAdmin.js';

export function Admin() {
  const { token, sellers, orders, message, isError, onboarding, unlock, addSeller, addMeal, createOnboarding, toggleApproval, lock } = useAdmin();

  return <>
    <Header><a href="/">← Back to menu</a></Header>
    <main className="wrap admin">
      <p className="eyebrow">TEST MODE ONLY</p><h1>Pilot administration</h1>
      <p>Only approve sellers after independently verifying their permits, kitchen, menu, and pickup arrangements. This tool does not verify them for you.</p>
      {message && <p role="status" className={isError ? 'notice error' : 'notice'}>{message}</p>}
      {!token ? <LoginForm onSubmit={unlock} /> : <div>
        <div className="admin-grid"><SellerForm onSubmit={addSeller} /><MealForm sellers={sellers} onSubmit={addMeal} /></div>
        <SellerList sellers={sellers} onboarding={onboarding} onCreateOnboarding={createOnboarding} onToggleApproval={toggleApproval} />
        <OrderList orders={orders} />
        <button className="link-button" onClick={lock}>Lock admin</button>
      </div>}
    </main>
  </>;
}
