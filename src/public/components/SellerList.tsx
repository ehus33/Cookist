import type { Seller } from '../types.js';

interface SellerListProps {
  sellers: Seller[];
  onboarding: Record<number, string>;
  onCreateOnboarding: (id: number) => void;
  onToggleApproval: (seller: Seller) => void;
}

export function SellerList({ sellers, onboarding, onCreateOnboarding, onToggleApproval }: SellerListProps) {
  return <section className="panel"><h2>Sellers</h2><p>Share the onboarding link privately with the seller. Approval is separate from Stripe onboarding.</p>
    {!sellers.length && <p>No sellers yet.</p>}
    {sellers.map(seller => <div className="admin-row" key={seller.id}>
      <strong>{seller.name} — {seller.approved ? 'approved' : 'not approved'}</strong>
      <p className="detail">{seller.kitchen} · Permit: {seller.permit_reference}</p>
      <button className="link-button" onClick={() => onCreateOnboarding(seller.id)}>Create onboarding link</button>
      <button className="link-button" onClick={() => onToggleApproval(seller)}>{seller.approved ? 'Suspend seller' : 'Approve seller'}</button>
      {onboarding[seller.id] && <a href={onboarding[seller.id]} target="_blank" rel="noopener noreferrer">Open or copy the single-use onboarding link</a>}
    </div>)}
  </section>;
}
