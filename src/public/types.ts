export interface Meal {
  id: number;
  name: string;
  description: string;
  allergens: string;
  pickup: string;
  price_cents: number;
  seller: string;
  kitchen: string;
}

export interface Seller {
  id: number;
  name: string;
  approved: number;
  kitchen: string;
  permit_reference: string;
}

export interface Order {
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
