export {};
const meals = document.querySelector<HTMLElement>('#meals')!;
const notice = document.querySelector<HTMLElement>('#notice')!;

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'An unexpected error occurred.';
}

function showNotice(message: string, error = false) {
  notice.textContent = message;
  notice.className = error ? 'notice error' : 'notice';
  notice.hidden = false;
}

function element<K extends keyof HTMLElementTagNameMap>(tag: K, text: string, className?: string): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.textContent = text;
  if (className) node.className = className;
  return node;
}

async function loadMeals() {
  try {
    const response = await fetch('/api/meals');
    if (!response.ok) throw new Error('Could not load the menu.');
    const data = await response.json();
    meals.replaceChildren();
    if (!data.length) {
      meals.append(element('p', 'No meals are listed yet. Our pilot menu is coming soon.', 'empty'));
      return;
    }
    for (const meal of data) {
      const card = element('article', '', 'meal-card');
      const art = element('div', '✳', 'meal-art');
      const content = element('div', '', 'meal-content');
      content.append(element('p', meal.seller, 'eyebrow'), element('h3', meal.name),
        element('p', meal.description), element('p', `Allergens: ${meal.allergens}`, 'detail'),
        element('p', `Pickup: ${meal.pickup}`, 'detail'), element('p', `Kitchen: ${meal.kitchen}`, 'detail'));
      const bottom = element('div', '', 'meal-bottom');
      bottom.append(element('strong', `$${(meal.price_cents / 100).toFixed(2)}`));
      const button = element('button', 'Test checkout', 'button small');
      button.addEventListener('click', async () => {
        button.disabled = true;
        try {
          const response = await fetch('/api/checkout', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ mealId: meal.id, quantity: 1 })
          });
          const data = await response.json();
          if (!response.ok) throw new Error(data.error);
          window.location.assign(data.url);
        } catch (error) {
          showNotice(errorMessage(error), true);
          button.disabled = false;
        }
      });
      bottom.append(button);
      content.append(bottom);
      card.append(art, content);
      meals.append(card);
    }
  } catch (error) {
    meals.replaceChildren(element('p', errorMessage(error), 'error'));
  }
}

const checkout = new URLSearchParams(location.search).get('checkout');
if (checkout === 'success') showNotice('Checkout completed. Payment confirmation is processed separately. This was a test; no food will be provided.');
if (checkout === 'cancelled') showNotice('Checkout cancelled. No test payment was made.');
loadMeals();
