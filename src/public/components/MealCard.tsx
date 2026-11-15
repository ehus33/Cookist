import type { Meal } from '../types.js';

interface MealCardProps {
  meal: Meal;
  disabled: boolean;
  onCheckout: (meal: Meal) => void;
}

export function MealCard({ meal, disabled, onCheckout }: MealCardProps) {
  return <article className="meal-card">
    <div className="meal-art">✳</div><div className="meal-content">
      <p className="eyebrow">{meal.seller}</p><h3>{meal.name}</h3><p>{meal.description}</p>
      <p className="detail">Allergens: {meal.allergens}</p><p className="detail">Pickup: {meal.pickup}</p>
      <p className="detail">Kitchen: {meal.kitchen}</p>
      <div className="meal-bottom"><strong>${(meal.price_cents / 100).toFixed(2)}</strong>
        <button className="button small" disabled={disabled} onClick={() => onCheckout(meal)}>Test checkout</button></div>
    </div>
  </article>;
}
