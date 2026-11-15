import type { FormEventHandler } from 'react';
import type { Seller } from '../types.js';

export function LoginForm({ onSubmit }: { onSubmit: FormEventHandler<HTMLFormElement> }) {
  return <form className="panel" onSubmit={onSubmit}>
    <h2>Administrator access</h2>
    <label>Admin token <input name="token" type="password" autoComplete="off" required /></label>
    <button className="button small">Unlock</button>
  </form>;
}

export function SellerForm({ onSubmit }: { onSubmit: FormEventHandler<HTMLFormElement> }) {
  return <form className="panel" onSubmit={onSubmit}>
    <h2>Add seller</h2>
    <label>Seller name <input name="name" maxLength={200} required /></label>
    <label>Approved kitchen name and address <input name="kitchen" maxLength={200} required /></label>
    <label>Permit reference <input name="permitReference" maxLength={200} required /></label>
    <button className="button small">Create test seller</button>
  </form>;
}

export function MealForm({ sellers, onSubmit }: { sellers: Seller[]; onSubmit: FormEventHandler<HTMLFormElement> }) {
  return <form className="panel" onSubmit={onSubmit}>
    <h2>Add meal</h2>
    <label>Seller <select name="sellerId" required>{sellers.map(seller => <option key={seller.id} value={seller.id}>{seller.name}</option>)}</select></label>
    <label>Meal name <input name="name" maxLength={500} required /></label>
    <label>Description and ingredients <textarea name="description" maxLength={500} required /></label>
    <label>Allergen information <input name="allergens" maxLength={500} required /></label>
    <label>Pickup location, date and time <input name="pickup" maxLength={500} required /></label>
    <label>Price in dollars <input name="price" type="number" min="0.50" max="10000" step="0.01" required /></label>
    <button className="button small">Add meal</button>
  </form>;
}
