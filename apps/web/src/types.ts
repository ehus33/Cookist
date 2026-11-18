export type Role = 'customer' | 'merchant' | 'courier';
export type Status = 'pending' | 'accepted' | 'preparing' | 'ready' | 'picked_up' | 'delivered' | 'cancelled';
export interface Account { id: string; name: string; storeId?: string | null }
export interface Accounts { customers: Account[]; merchants: Account[]; couriers: Account[] }
export interface Session { token: string; role: Role; actorId: string }
export interface MenuItem {
  id: string; name: string; description: string; category: string;
  priceCents: number; emoji: string; available: boolean;
}
export interface Store {
  id: string; name: string; cuisine: string; description: string;
  rating: number; reviewCount: number; distanceMiles: number; etaMinutes: number;
  deliveryFeeCents: number; minimumCents: number; isOpen: boolean;
  emoji: string; color: string; tags: string[]; lat: number; lng: number; menu: MenuItem[];
}
export interface SearchResult {
  query: string;
  suggestions: { type: 'query' | 'store'; text: string; storeId: string | null }[];
  stores: Store[];
}
export interface Totals {
  subtotalCents: number; taxCents: number; deliveryFeeCents: number; serviceFeeCents: number; totalCents: number;
}
export interface Order {
  id: string; storeId: string; storeName: string; customerId: string; customerName: string;
  deliveryAddress: string;
  items: { menuItemId: string; name: string; quantity: number; unitPriceCents: number }[];
  totals: Totals; status: Status;
  payment: { id: string; status: 'captured' | 'refunded'; method: 'simulated' };
  courierId: string | null; courierName: string | null;
  location: { lat: number; lng: number; updatedAt: string; source?: 'live' } | null;
  createdAt: string; updatedAt: string;
  events: { status: Status; at: string; note: string }[];
}
export interface Cart { storeId: string; items: Record<string, number> }
