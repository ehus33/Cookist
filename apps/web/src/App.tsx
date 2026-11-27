import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, errorMessage } from './api';
import type { Session } from './types';
import { SiteEditor } from './SiteEditor';
import { blockError, isPageBlock } from './pageBlocks';
import type { PageBlock } from './pageBlocks';
import { DynamicEditor } from './DynamicEditor';
import { DatabaseBilling } from './DatabaseBilling';
import { DynamicAdmin } from './DynamicAdmin';
import { dynamicError, isDynamicConfig } from './dynamicApps';
import type { DynamicConfig } from './dynamicApps';
import '../../../services/gateway/site_styles.css';

type DeploymentTemplate = 'blank' | 'business' | 'storefront' | 'builder';
type ProductDraft = { id: number; name: string; description: string; price: string; paymentLink: string };
type StoreProduct = { name: string; description: string; priceCents: number; paymentLink: string };
function newProduct(id: number): ProductDraft {
  return { id, name: '', description: '', price: '', paymentLink: '' };
}
function productPriceCents(price: string): number | null {
  if (!/^\d{1,6}(?:\.\d{1,2})?$/.test(price)) return null;
  const [dollars, cents = ''] = price.split('.');
  const value = Number(dollars) * 100 + Number(cents.padEnd(2, '0'));
  return value >= 1 && value <= 99999999 ? value : null;
}
function isPaymentLink(link: string): boolean {
  return /^https:\/\/buy\.stripe\.com\/(?:test_)?[A-Za-z0-9]+$/.test(link) && link.length <= 200;
}
type SiteStyle = 'classic' | 'modern' | 'warm' | 'bold';
const siteStyles: { id: SiteStyle; name: string; description: string }[] = [
  { id: 'classic', name: 'Classic', description: 'Simple, familiar, and distraction-free.' },
  { id: 'modern', name: 'Modern', description: 'Clean blue accents and rounded cards.' },
  { id: 'warm', name: 'Warm', description: 'Cream tones and elegant serif typography.' },
  { id: 'bold', name: 'Bold', description: 'Dark surfaces and vivid purple accents.' },
];
function isSiteStyle(value: unknown): value is SiteStyle {
  return siteStyles.some(style => style.id === value);
}
type Deployment = { id: string; name: string; region: string; status: 'running' | 'ready' | 'failed' | 'deleting' | 'cleanup_failed'; url?: string; style?: SiteStyle; canEdit?: boolean; canData?: boolean; lastUpdateError?: string };
type BuilderDesign = { name: string; region: string; template: 'builder'; style: SiteStyle; businessName: string; listTitle: string; items: string; blocks: PageBlock[]; products?: StoreProduct[]; draftProducts?: Omit<ProductDraft, 'id'>[]; draftDeploymentId?: string; dynamic?: DynamicConfig };
type Lead = { id: string; formId: string; kind: string; name: string; email: string; message: string; createdAt: string };
function isLead(value: unknown): value is Lead {
  if (!value || typeof value !== 'object') return false;
  const lead = value as Record<string, unknown>;
  return ['id', 'formId', 'kind', 'name', 'email', 'message', 'createdAt'].every(key => typeof lead[key] === 'string');
}
function isBuilderDesign(value: unknown): value is BuilderDesign {
  if (!value || typeof value !== 'object') return false;
  const design = value as Record<string, unknown>;
  return ['name', 'region', 'businessName', 'listTitle', 'items'].every(key => typeof design[key] === 'string')
    && design.template === 'builder' && isSiteStyle(design.style)
    && (design.dynamic === undefined || isDynamicConfig(design.dynamic))
    && (design.draftDeploymentId === undefined || (typeof design.draftDeploymentId === 'string' && /^[a-f0-9]{32}$/.test(design.draftDeploymentId)))
    && Array.isArray(design.blocks) && design.blocks.length <= 40 && design.blocks.every(isPageBlock)
    && (design.products === undefined || (Array.isArray(design.products) && design.products.every(product =>
      product && typeof product === 'object' && typeof product.name === 'string' && typeof product.description === 'string'
      && typeof product.priceCents === 'number' && Number.isInteger(product.priceCents) && typeof product.paymentLink === 'string')))
    && (design.draftProducts === undefined || (Array.isArray(design.draftProducts) && design.draftProducts.every(product =>
      product && typeof product === 'object' && ['name', 'description', 'price', 'paymentLink'].every(key => typeof product[key] === 'string'))));
}
type DeploymentAccess = { role: string; subscribed: boolean; paymentRequired: boolean; canList: boolean; canCreate: boolean; canDelete: boolean; canCheckout?: boolean; canManageBilling?: boolean; dynamicEnabled?: boolean; canUseDynamic?: boolean; databaseSubscribed?: boolean; databaseBillingAvailable?: boolean };

function isDeploymentAccess(value: unknown): value is DeploymentAccess {
  if (!value || typeof value !== 'object') return false;
  const access = value as Record<string, unknown>;
  return typeof access.role === 'string'
    && ['platform_owner', 'customer_admin', 'deployer', 'viewer', 'none'].includes(access.role)
    && typeof access.subscribed === 'boolean' && typeof access.canList === 'boolean'
    && typeof access.canCreate === 'boolean' && typeof access.canDelete === 'boolean'
    && typeof access.paymentRequired === 'boolean'
    && (access.canCheckout === undefined || typeof access.canCheckout === 'boolean')
    && (access.canManageBilling === undefined || typeof access.canManageBilling === 'boolean')
    && (access.dynamicEnabled === undefined || typeof access.dynamicEnabled === 'boolean')
    && (access.canUseDynamic === undefined || typeof access.canUseDynamic === 'boolean')
    && (access.databaseSubscribed === undefined || typeof access.databaseSubscribed === 'boolean')
    && (access.databaseBillingAvailable === undefined || typeof access.databaseBillingAvailable === 'boolean');
}

function isDeployment(value: unknown): value is Deployment {
  if (!value || typeof value !== 'object') return false;
  const d = value as Record<string, unknown>;
  return typeof d.id === 'string' && typeof d.name === 'string' && typeof d.region === 'string'
    && (d.status === 'running' || d.status === 'ready' || d.status === 'failed' || d.status === 'deleting' || d.status === 'cleanup_failed')
    && (d.style === undefined || isSiteStyle(d.style))
    && (d.canEdit === undefined || typeof d.canEdit === 'boolean')
    && (d.canData === undefined || typeof d.canData === 'boolean')
    && (d.lastUpdateError === undefined || typeof d.lastUpdateError === 'string')
    && (d.url === undefined || (typeof d.url === 'string' && /^https:\/\/[a-z0-9-]+\.cloudfront\.net$/.test(d.url)));
}

const awsRegions = [
  ['us-east-1', 'US East (N. Virginia)'],
  ['us-east-2', 'US East (Ohio)'],
  ['us-west-1', 'US West (N. California)'],
  ['us-west-2', 'US West (Oregon)'],
  ['af-south-1', 'Africa (Cape Town)'],
  ['ap-east-1', 'Asia Pacific (Hong Kong)'],
  ['ap-east-2', 'Asia Pacific (Taipei)'],
  ['ap-south-1', 'Asia Pacific (Mumbai)'],
  ['ap-south-2', 'Asia Pacific (Hyderabad)'],
  ['ap-southeast-1', 'Asia Pacific (Singapore)'],
  ['ap-southeast-2', 'Asia Pacific (Sydney)'],
  ['ap-southeast-3', 'Asia Pacific (Jakarta)'],
  ['ap-southeast-4', 'Asia Pacific (Melbourne)'],
  ['ap-southeast-5', 'Asia Pacific (Malaysia)'],
  ['ap-southeast-6', 'Asia Pacific (New Zealand)'],
  ['ap-southeast-7', 'Asia Pacific (Thailand)'],
  ['ap-northeast-1', 'Asia Pacific (Tokyo)'],
  ['ap-northeast-2', 'Asia Pacific (Seoul)'],
  ['ap-northeast-3', 'Asia Pacific (Osaka)'],
  ['ca-central-1', 'Canada (Central)'],
  ['ca-west-1', 'Canada West (Calgary)'],
  ['eu-central-1', 'Europe (Frankfurt)'],
  ['eu-central-2', 'Europe (Zurich)'],
  ['eu-west-1', 'Europe (Ireland)'],
  ['eu-west-2', 'Europe (London)'],
  ['eu-west-3', 'Europe (Paris)'],
  ['eu-south-1', 'Europe (Milan)'],
  ['eu-south-2', 'Europe (Spain)'],
  ['eu-north-1', 'Europe (Stockholm)'],
  ['il-central-1', 'Israel (Tel Aviv)'],
  ['mx-central-1', 'Mexico (Central)'],
  ['me-south-1', 'Middle East (Bahrain)'],
  ['me-central-1', 'Middle East (UAE)'],
  ['sa-east-1', 'South America (São Paulo)'],
];

export function App({ authenticatedSession, onSignOut, authError }: {
  authenticatedSession?: Session; onSignOut?: () => void; authError?: string;
} = {}) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [region, setRegion] = useState('us-west-2');
  const [template, setTemplate] = useState<DeploymentTemplate>('blank');
  const [siteStyle, setSiteStyle] = useState<SiteStyle>('classic');
  const [businessName, setBusinessName] = useState('');
  const [listTitle, setListTitle] = useState('Services');
  const [items, setItems] = useState('');
  const [products, setProducts] = useState<ProductDraft[]>([newProduct(1)]);
  const [blocks, setBlocks] = useState<PageBlock[]>([]);
  const [dynamic, setDynamic] = useState<DynamicConfig | undefined>();
  const [dataTarget, setDataTarget] = useState<Deployment | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [draftBusy, setDraftBusy] = useState(false);
  const [editorRevision, setEditorRevision] = useState(0);
  const [inbox, setInbox] = useState<{ deployment: Deployment; leads: Lead[] } | null>(null);
  const nextProductId = useRef(2);
  const [message, setMessage] = useState<{ text: string; error: boolean } | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [loadError, setLoadError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Deployment | null>(null);
  const [access, setAccess] = useState<DeploymentAccess | null>(null);
  const [loadedActor, setLoadedActor] = useState<string | null>(null);
  const [billingPending, setBillingPending] = useState(false);
  const [billingLink, setBillingLink] = useState<{ url: string; portal: boolean } | null>(null);
  const currentAccess = loadedActor === authenticatedSession?.actorId ? access : null;
  const previewItems = items.split('\n').map(item => item.trim()).filter(Boolean);
  const usesProducts = template === 'storefront' || (template === 'builder' && blocks.some(block => block.type === 'products'));
  const visibleDeployments = loadedActor === authenticatedSession?.actorId ? deployments : [];
  const newDeploymentButton = useRef<HTMLButtonElement>(null);
  const requestVersion = useRef(0);
  const sessionVersion = useRef(0);

  useEffect(() => {
    sessionVersion.current++;
    requestVersion.current++;
    setAccess(null);
    setLoadedActor(null);
    setDeployments([]);
    setCreating(false);
    setDeleteTarget(null);
    setMessage(null);
    setLoadError('');
    setSubmitting(false);
    setDeleting(null);
    setBillingPending(false);
    setBillingLink(null);
    setProducts([newProduct(1)]);
    setBlocks([]);
    setDynamic(undefined);
    setDataTarget(null);
    setEditingId(null);
    setInbox(null);
    setDraftBusy(false);
    setName('');
    setBusinessName('');
    setListTitle('Services');
    setItems('');
    setTemplate('blank');
    setSiteStyle('classic');
    setRegion('us-west-2');
    if (!authenticatedSession) return;
    const session = authenticatedSession;
    const controller = new AbortController();
    let loading = false;
    async function load() {
      if (loading) return;
      loading = true;
      const version = ++requestVersion.current;
      try {
        const permissions = await api<unknown>('/api/deployment-access', session, { signal: controller.signal });
        if (!isDeploymentAccess(permissions)) throw new Error('The server returned invalid access permissions.');
        const result = permissions.canList
          ? await api<unknown>('/api/deployments', session, { signal: controller.signal }) : [];
        if (!Array.isArray(result) || !result.every(isDeployment)) throw new Error('The server returned an invalid deployment list.');
        if (!controller.signal.aborted && version === requestVersion.current) {
          setDeployments(result);
          setAccess(permissions);
          setLoadedActor(session.actorId);
          if (!permissions.canCreate) setCreating(false);
          if (!permissions.canDelete) setDeleteTarget(null);
          if (!permissions.canCheckout && !permissions.canManageBilling) setBillingLink(null);
          setLoadError('');
        }
      } catch (error) {
        if (!controller.signal.aborted && version === requestVersion.current) {
          setAccess(null);
          setDeployments([]);
          setCreating(false);
          setDeleteTarget(null);
          setBillingLink(null);
          setLoadError(errorMessage(error));
        }
      } finally {
        loading = false;
      }
    }
    void load();
    const timer = window.setInterval(() => { void load(); }, 5000);
    return () => { sessionVersion.current++; controller.abort(); window.clearInterval(timer); };
  }, [authenticatedSession]);

  async function deploy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!name.trim() || !region.trim() || (template === 'business' && (!businessName.trim() || !listTitle.trim()))) {
      setMessage({ text: 'Complete all required fields before deploying.', error: true });
      return;
    }
    if (!awsRegions.some(([code]) => code === region)) {
      setMessage({ text: 'Select an available AWS region.', error: true });
      return;
    }
    const submittedProducts: StoreProduct[] = [];
    if (template === 'builder') {
      const error = dynamicError(dynamic) ?? blockError(blocks, dynamic?.collections);
      if (error) {
        setMessage({ text: error, error: true });
        return;
      }
    }
    if (usesProducts) {
      if (!businessName.trim() || products.length < 1 || products.length > 20) {
        setMessage({ text: 'Enter a store name and between 1 and 20 products.', error: true });
        return;
      }
      for (const [index, product] of products.entries()) {
        const priceCents = productPriceCents(product.price);
        if (!product.name.trim() || priceCents === null || !isPaymentLink(product.paymentLink.trim())) {
          setMessage({ text: `Product ${index + 1} needs a name, a USD price from $0.01 to $999999.99, and a plain https://buy.stripe.com/ Payment Link.`, error: true });
          return;
        }
        submittedProducts.push({ name: product.name, description: product.description, priceCents, paymentLink: product.paymentLink.trim() });
      }
    }
    if (!authenticatedSession || !currentAccess?.canCreate) {
      setMessage({ text: 'Your account does not currently have permission to deploy.', error: true });
      return;
    }
    setSubmitting(true);
    setMessage(null);
    const version = sessionVersion.current;
    try {
      const result = await api<unknown>(editingId ? `/api/deployments/${editingId}/design` : '/api/deployments', authenticatedSession, {
        method: editingId ? 'PATCH' : 'POST', body: { name, region, template, style: siteStyle, businessName, listTitle, items,
          ...(usesProducts ? { products: submittedProducts } : {}),
          ...(template === 'builder' ? { blocks, ...(dynamic ? { dynamic } : {}) } : {}) },
      });
      if (version !== sessionVersion.current) return;
      if (!isDeployment(result)) throw new Error('The server returned an invalid deployment.');
      requestVersion.current++;
      setDeployments(previous => [result, ...previous.filter(item => item.id !== result.id)]);
      setCreating(false);
      setEditingId(null);
      setLoadError('');
      setMessage({ text: editingId ? 'Site update started. The existing URL and infrastructure are retained.' : 'Deployment started. It may take several minutes.', error: false });
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: `Could not confirm deployment: ${errorMessage(error)} Check the deployment list before trying again.`, error: true });
    } finally {
      if (version === sessionVersion.current) setSubmitting(false);
    }
  }

  function loadDesign(design: BuilderDesign) {
    if (!awsRegions.some(([code]) => code === design.region)) throw new Error('The saved design has an unsupported region.');
    setName(design.name);
    setRegion(design.region);
    setTemplate('builder');
    setSiteStyle(design.style);
    setBusinessName(design.businessName);
    setListTitle(design.listTitle);
    setItems(design.items);
    setBlocks(design.blocks);
    setDynamic(design.dynamic);
    const savedProducts = design.draftProducts ?? design.products?.map(product => ({
      name: product.name, description: product.description, price: (product.priceCents / 100).toFixed(2), paymentLink: product.paymentLink,
    })) ?? [];
    setProducts(savedProducts.length ? savedProducts.map((product, index) => ({ ...product, id: index + 1 })) : [newProduct(1)]);
    nextProductId.current = Math.max(2, savedProducts.length + 1);
    setCreating(true);
    setEditorRevision(value => value + 1);
  }

  async function draftAction(action: 'save' | 'load' | 'delete') {
    if (!authenticatedSession || draftBusy || !currentAccess?.canCreate) return;
    const version = sessionVersion.current;
    setDraftBusy(true);
    setMessage(null);
    try {
      const result = await api<unknown>('/api/builder-draft', authenticatedSession, {
        method: action === 'save' ? 'PUT' : action === 'delete' ? 'DELETE' : 'GET',
        ...(action === 'save' ? { body: { name, region, template: 'builder', style: siteStyle, businessName, listTitle, items, blocks,
          ...(editingId ? { draftDeploymentId: editingId } : {}),
          ...(dynamic ? { dynamic } : {}),
          draftProducts: products.map(({ name, description, price, paymentLink }) => ({ name, description, price, paymentLink })) } } : {}),
      });
      if (version !== sessionVersion.current) return;
      if (action === 'save' && (!result || typeof result !== 'object' || !('draft' in result) || !isBuilderDesign(result.draft))) {
        throw new Error('The server did not confirm a valid saved draft.');
      }
      if (action === 'delete' && (!result || typeof result !== 'object' || !('draft' in result) || result.draft !== null)) {
        throw new Error('The server did not confirm draft deletion.');
      }
      if (action === 'load') {
        if (!result || typeof result !== 'object' || !('draft' in result)) throw new Error('The server returned an invalid draft.');
        if (result.draft === null) throw new Error('You do not have a saved draft yet.');
        const draft = result.draft;
        if (!isBuilderDesign(draft)) throw new Error('The server returned an invalid draft.');
        if (draft.draftDeploymentId && !visibleDeployments.some(deployment => deployment.id === draft.draftDeploymentId && deployment.canEdit)) {
          throw new Error('The saved draft targets a site that is unavailable for editing. Check its deployment status.');
        }
        loadDesign(draft);
        setEditingId(draft.draftDeploymentId ?? null);
      }
      setMessage({ text: action === 'save' ? 'Draft saved to your account. It does not change the published site.' : action === 'load' ? 'Saved draft loaded. Review it before publishing.' : 'Saved draft deleted.', error: false });
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: errorMessage(error), error: true });
    } finally {
      if (version === sessionVersion.current) setDraftBusy(false);
    }
  }

  async function editSite(target: Deployment) {
    if (!authenticatedSession || draftBusy || !currentAccess?.canCreate) return;
    const version = sessionVersion.current;
    setDraftBusy(true);
    setMessage(null);
    try {
      const design = await api<unknown>(`/api/deployments/${target.id}/design`, authenticatedSession);
      if (version !== sessionVersion.current) return;
      if (!isBuilderDesign(design)) throw new Error('The server returned an invalid saved design.');
      loadDesign(design);
      setEditingId(target.id);
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: errorMessage(error), error: true });
    } finally {
      if (version === sessionVersion.current) setDraftBusy(false);
    }
  }

  async function loadInbox(target: Deployment) {
    if (!authenticatedSession || draftBusy || !currentAccess?.canDelete) return;
    const version = sessionVersion.current;
    setDraftBusy(true);
    setMessage(null);
    try {
      const leads = await api<unknown>(`/api/deployments/${target.id}/submissions`, authenticatedSession);
      if (version !== sessionVersion.current) return;
      if (!Array.isArray(leads) || !leads.every(isLead)) throw new Error('The server returned an invalid inbox.');
      setInbox({ deployment: target, leads });
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: errorMessage(error), error: true });
    } finally {
      if (version === sessionVersion.current) setDraftBusy(false);
    }
  }

  async function deleteLead(id: string) {
    if (!authenticatedSession || !inbox || draftBusy || !currentAccess?.canDelete) return;
    const version = sessionVersion.current;
    setDraftBusy(true);
    try {
      const result = await api<unknown>(`/api/deployments/${inbox.deployment.id}/submissions/${id}`, authenticatedSession, { method: 'DELETE' });
      if (!result || typeof result !== 'object' || !('deleted' in result) || result.deleted !== true) throw new Error('The server did not confirm submission deletion.');
      if (version === sessionVersion.current) setInbox(current => current ? { ...current, leads: current.leads.filter(lead => lead.id !== id) } : null);
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: errorMessage(error), error: true });
    } finally {
      if (version === sessionVersion.current) setDraftBusy(false);
    }
  }

  function updateProduct(id: number, field: 'name' | 'description' | 'price' | 'paymentLink', value: string) {
    setProducts(previous => previous.map(product => product.id === id ? { ...product, [field]: value } : product));
  }

  async function deleteDeployment(target: Deployment) {
    if (!authenticatedSession || !currentAccess?.canDelete || deleting) return;
    setDeleting(target.id);
    setMessage(null);
    const version = sessionVersion.current;
    try {
      const result = await api<unknown>(`/api/deployments/${encodeURIComponent(target.id)}`, authenticatedSession, { method: 'DELETE' });
      if (version !== sessionVersion.current) return;
      if (!isDeployment(result) || result.id !== target.id || result.status !== 'deleting') {
        throw new Error('The server returned an invalid cleanup response.');
      }
      requestVersion.current++;
      setDeployments(previous => previous.map(item => item.id === result.id ? result : item));
      setDeleteTarget(null);
      setMessage({ text: 'Resource deletion started. CloudFront cleanup may take several minutes. The deployment disappears only after cleanup succeeds.', error: false });
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: `Could not confirm cleanup: ${errorMessage(error)} Check the deployment list before trying again.`, error: true });
    } finally {
      if (version === sessionVersion.current) setDeleting(null);
    }
  }

  async function startBilling(portal: boolean) {
    if (!authenticatedSession || billingPending ||
      !(portal ? currentAccess?.canManageBilling : currentAccess?.canCheckout)) return;
    const version = sessionVersion.current;
    setBillingPending(true);
    setBillingLink(null);
    setMessage(null);
    try {
      const result = await api<unknown>(`/api/billing/${portal ? 'portal' : 'checkout'}`, authenticatedSession, { method: 'POST' });
      if (version !== sessionVersion.current) return;
      if (!result || typeof result !== 'object' || !('url' in result) || typeof result.url !== 'string') {
        throw new Error('The server returned an invalid billing link.');
      }
      const url = new URL(result.url);
      if (url.protocol !== 'https:' || url.host !== (portal ? 'billing.stripe.com' : 'checkout.stripe.com') || url.username || url.password) {
        throw new Error('The server returned an invalid Stripe URL.');
      }
      setBillingLink({ url: result.url, portal });
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: `Could not start billing: ${errorMessage(error)}`, error: true });
    } finally {
      if (version === sessionVersion.current) setBillingPending(false);
    }
  }

  const catalogPreview = <>
    <p className="store-checkout-note">Secure checkout by Stripe. Final price, availability, shipping and taxes are confirmed at checkout.</p>
    <ul className="store-products">{products.map(product => {
      const priceCents = productPriceCents(product.price);
      return <li key={product.id}>
        <h3>{product.name.trim() || 'Your product'}</h3>
        <p className="store-description">{product.description.trim()}</p>
        <p className="store-price">{priceCents === null ? 'Enter a price' : `$${(priceCents / 100).toFixed(2)} USD`}</p>
        {product.paymentLink.trim().startsWith('https://buy.stripe.com/test_') && <p className="store-test-notice">Test checkout - no real charge</p>}
        <button type="button" className="store-buy" disabled>Buy now</button>
      </li>;
    })}</ul>
  </>;

  return <main className={`workspace${creating && template === 'builder' ? ' has-page-builder' : ''}`}>
    <header>
      <div>
        <h1>Cookist</h1>
        <p>A Machi service</p>
      </div>
      {authenticatedSession && <button type="button" onClick={onSignOut}>Sign out</button>}
    </header>
    {authError && <p className="error" role="alert">{authError}</p>}
    <section className="deployments">
      <div className="section-heading">
        <div><h2>Deployments</h2><p>Deploy a site to your AWS account.</p></div>
        <button ref={newDeploymentButton} type="button" className="primary" disabled={!!authenticatedSession && !currentAccess?.canCreate}
          onClick={() => { setCreating(true); setEditingId(null); setMessage(null); }}>
          <span aria-hidden="true">+</span> New deployment
        </button>
      </div>
      {!authenticatedSession && <p className="deployment-notice">Managed deployments require signing in with an authorized account.</p>}
      {authenticatedSession && !currentAccess && !loadError && <p role="status">Checking deployment access...</p>}
      {currentAccess && !currentAccess.canList && <section aria-label="Access required">
        <h3>{currentAccess.paymentRequired ? 'Paid access required' : 'Access required'}</h3>
        <p>Your account has not been assigned to an organization. Contact the operator for membership and billing permissions.</p>
      </section>}
      {currentAccess?.canList && currentAccess.paymentRequired && <section aria-label="Payment required">
        <h3>Paid access required</h3>
        <p className="deployment-notice">An active subscription is required to use deployment features.
          Existing deployment records remain available for recovery and, if your role permits,
          deletion to stop further usage charges.
          {!currentAccess.canCheckout && !currentAccess.canManageBilling && ' Ask your organization administrator to arrange payment.'}</p>
      </section>}
      {currentAccess?.canCheckout && <button type="button" disabled={billingPending} onClick={() => void startBilling(false)}>
        {billingPending ? 'Preparing billing...' : 'Subscribe with Stripe'}
      </button>}
      {currentAccess?.canManageBilling && <button type="button" disabled={billingPending} onClick={() => void startBilling(true)}>
        Manage billing
      </button>}
      {billingLink && currentAccess && <p>
        <a href={billingLink.url} rel="noreferrer">{billingLink.portal ? 'Continue to Stripe billing portal' : 'Continue to secure Stripe checkout'}</a>
        {!billingLink.portal && ' Access unlocks only after Stripe confirms payment, not when you return from checkout.'}
      </p>}
      {currentAccess?.role === 'viewer' && <p className="deployment-notice">Your role provides read-only deployment access.</p>}
      {authenticatedSession && currentAccess?.role === 'customer_admin' && currentAccess.databaseBillingAvailable && <DatabaseBilling session={authenticatedSession} />}
      {currentAccess?.canCreate && !creating && <button type="button" className="secondary" disabled={draftBusy} onClick={() => void draftAction('load')}>Load saved builder draft</button>}
      {loadError && <p role="alert">{loadError}</p>}
      {visibleDeployments.length > 0 && <ul className="deployment-list">
        {visibleDeployments.map(deployment => <li key={deployment.id}>
          <strong>{deployment.name}</strong> <span>{deployment.region} &mdash; {deployment.status}</span>
          {deployment.style && <small className="site-style-label">Style: {deployment.style}</small>}
          {deployment.lastUpdateError && <small role="alert">{deployment.lastUpdateError}</small>}
          {deployment.canEdit && currentAccess?.canCreate && <button type="button" disabled={draftBusy || submitting} onClick={() => void editSite(deployment)}>Edit {deployment.name}</button>}
          {currentAccess?.canDelete && <button type="button" disabled={draftBusy} onClick={() => void loadInbox(deployment)}>Submissions for {deployment.name}</button>}
          {deployment.canData && currentAccess?.canDelete && <button type="button" onClick={() => setDataTarget(deployment)}>Data for {deployment.name}</button>}
          {deployment.url && currentAccess && !currentAccess.paymentRequired &&
            <a href={deployment.url} target="_blank" rel="noreferrer">Open site</a>}
          {deployment.status === 'failed' && <small>Deployment failed. An operator must inspect the private Terraform log and state.</small>}
          {deployment.status === 'running' && <small>If the server was interrupted, an operator must check the worker and state before trying again.</small>}
          {deployment.status === 'deleting' && <small>Deleting AWS resources. State is retained until cleanup succeeds.</small>}
          {deployment.status === 'cleanup_failed' && <small>Cleanup failed. State is retained; inspect the private Terraform log, fix the cause, then retry cleanup.</small>}
          {currentAccess?.canDelete && (deployment.status === 'ready' || deployment.status === 'failed' || deployment.status === 'cleanup_failed') &&
            <button type="button" disabled={!!deleting} onClick={() => { setDeleteTarget(deployment); setMessage(null); }}
              aria-label={`${deployment.status === 'cleanup_failed' ? 'Retry cleanup for' : 'Delete'} ${deployment.name}`}>
              {deployment.status === 'cleanup_failed' ? 'Retry cleanup' : 'Delete resources'}
            </button>}
        </li>)}
      </ul>}
      {dataTarget && authenticatedSession && currentAccess?.canDelete && <DynamicAdmin key={`${authenticatedSession.actorId}-${dataTarget.id}`} deployment={dataTarget} session={authenticatedSession} onClose={() => setDataTarget(null)} />}
      {inbox && currentAccess?.canDelete && <section className="builder-inbox" aria-label={`Submissions for ${inbox.deployment.name}`}>
        <h3>Submissions for {inbox.deployment.name}</h3>
        <p>Private to your organization. Delete entries when no longer needed. This inbox does not send email automatically.</p>
        <button type="button" className="secondary" onClick={() => setInbox(null)}>Close inbox</button>
        {!inbox.leads.length ? <p>No submissions yet.</p> : <ul>{inbox.leads.map(lead => <li key={lead.id}>
          <strong>{lead.name}</strong> — {lead.kind}<p>{lead.email}</p><p>{lead.message}</p><small>{lead.createdAt}</small>
          <button type="button" disabled={draftBusy} onClick={() => void deleteLead(lead.id)}>Delete submission from {lead.name}</button>
        </li>)}</ul>}
      </section>}
      {deleteTarget && currentAccess?.canDelete && <section role="region" aria-label={`Confirm deletion of ${deleteTarget.name}`}>
        <h3>Delete {deleteTarget.name}?</h3>
        <p>This permanently deletes this deployment's AWS resources, site content, saved design, and submissions. After successful cleanup,
          its saved state is removed and its deployment slot is freed. Previously incurred AWS charges are not refunded.</p>
        <button type="button" className="primary" disabled={!!deleting} onClick={() => void deleteDeployment(deleteTarget)}>
          {deleting ? 'Starting cleanup...' : 'Confirm delete'}
        </button>
        <button type="button" disabled={!!deleting} onClick={() => setDeleteTarget(null)}>Cancel deletion</button>
      </section>}
      {!creating && visibleDeployments.length === 0 && (!authenticatedSession || currentAccess?.canList) && <div className="empty-state">
        <h3>Start with a blank slate</h3>
        <p>Choose a business listing, or sell products with a Stripe-powered storefront.</p>
      </div>}
      {!creating && message && <p role={message.error ? 'alert' : 'status'}>{message.text}</p>}
      {creating && (!authenticatedSession || currentAccess?.canCreate) && <form className="deployment-form" onSubmit={event => void deploy(event)}>
        <h3>{editingId ? 'Edit existing site' : 'Create a Terraform deployment'}</h3>
        <label>Deployment name<input required maxLength={80} value={name} onChange={event => setName(event.target.value)} /></label>
        <label>AWS region
          <select required value={region} disabled={!!editingId} onChange={event => setRegion(event.target.value)}>
            {awsRegions.map(([code, location]) => <option key={code} value={code}>{location} ({code})</option>)}
          </select>
        </label>
        <p>Some regions must be enabled in your AWS account. CloudFront serves the site globally.</p>
        <fieldset disabled={!!editingId}>
          <legend>Choose a template</legend>
          <label className="template-choice">
            <input type="radio" name="template" checked={template === 'blank'} onChange={() => setTemplate('blank')} />
            <span><strong>Blank slate</strong><small>Start with an empty site.</small></span>
          </label>
          <label className="template-choice">
            <input type="radio" name="template" checked={template === 'business'} onChange={() => setTemplate('business')} />
            <span><strong>Business listing</strong><small>A simple menu, service list, or other offering.</small></span>
          </label>
          <label className="template-choice">
            <input type="radio" name="template" checked={template === 'storefront'} onChange={() => setTemplate('storefront')} />
            <span><strong>Storefront</strong><small>Sell products using your own Stripe Payment Links.</small></span>
          </label>
          <label className="template-choice">
            <input type="radio" name="template" checked={template === 'builder'} onChange={() => setTemplate('builder')} />
            <span><strong>Visual builder</strong><small>Drag and drop elements to design your own page.</small></span>
          </label>
        </fieldset>
        {template === 'business' && <div className="business-fields">
          <label>Business name<input required maxLength={80} value={businessName} onChange={event => setBusinessName(event.target.value)} /></label>
          <label>List title<input required maxLength={80} value={listTitle} onChange={event => setListTitle(event.target.value)} /></label>
          <label>Items, one per line<textarea maxLength={2000} rows={4} value={items} onChange={event => setItems(event.target.value)} /></label>
        </div>}
        {usesProducts && <div className="storefront-fields">
          <label>Store name<input required maxLength={80} value={businessName} onChange={event => setBusinessName(event.target.value)} /></label>
          <p>Create a one-time Payment Link for each product in <a href="https://dashboard.stripe.com/payment_links" target="_blank" rel="noreferrer">your Stripe account</a>,
            then paste it below. Sales go to that Stripe account, not to Cookist subscription billing.
            Match the displayed price to the link. Configure shipping, taxes, receipts and inventory limits in Stripe; fulfill orders and manage refunds there.
            Test links do not collect real money; use live links only when your seller account is ready.</p>
          {products.map((product, index) => <fieldset className="product-editor" key={product.id} disabled={submitting}>
            <legend>Product {index + 1}</legend>
            <label>Product {index + 1} name<input required maxLength={80} value={product.name} onChange={event => updateProduct(product.id, 'name', event.target.value)} /></label>
            <label>Product {index + 1} description<textarea maxLength={500} rows={3} value={product.description} onChange={event => updateProduct(product.id, 'description', event.target.value)} /></label>
            <label>Product {index + 1} price (USD)<input required inputMode="decimal" placeholder="19.99" maxLength={9} pattern="[0-9]{1,6}([.][0-9]{1,2})?" value={product.price} onChange={event => updateProduct(product.id, 'price', event.target.value)} /></label>
            <label>Product {index + 1} Stripe Payment Link<input required type="url" maxLength={200} placeholder="https://buy.stripe.com/..." value={product.paymentLink} onChange={event => updateProduct(product.id, 'paymentLink', event.target.value)} /></label>
            {product.paymentLink.trim().startsWith('https://buy.stripe.com/test_') && <small>Test checkout - no real charge</small>}
            <button type="button" className="secondary" disabled={products.length === 1} onClick={() => setProducts(previous => previous.filter(item => item.id !== product.id))}>Remove product {index + 1}</button>
          </fieldset>)}
          <button type="button" className="secondary" disabled={submitting || products.length >= 20} onClick={() => {
            const product = newProduct(nextProductId.current++);
            setProducts(previous => [...previous, product]);
          }}>Add product</button>
        </div>}
        <fieldset>
          <legend>Choose a site style</legend>
          <div className="site-style-options">
            {siteStyles.map(style => <label key={style.id} className={`site-style-choice${siteStyle === style.id ? ' is-selected' : ''}`}>
              <span className={`site-style-swatch generated-site site-style-${style.id}`} aria-hidden="true">
                <span className="swatch-title">Your site</span>
                <span className="swatch-card"><span /><span /></span>
              </span>
              <span className="site-style-choice-heading">
                <input type="radio" name="siteStyle" checked={siteStyle === style.id} disabled={submitting} onChange={() => setSiteStyle(style.id)} />
                <strong>{style.name}</strong>
              </span>
              <small>{style.description}</small>
            </label>)}
          </div>
        </fieldset>
        {template === 'builder' && <div className="builder-draft-actions">
          <button type="button" className="secondary" disabled={draftBusy || submitting} onClick={() => void draftAction('save')}>Save draft</button>
          <button type="button" className="secondary" disabled={draftBusy || submitting} onClick={() => void draftAction('load')}>Load draft</button>
          <button type="button" className="secondary" disabled={draftBusy || submitting} onClick={() => void draftAction('delete')}>Delete saved draft</button>
          <p>One private draft per account and organization. Saving is explicit; unsaved changes are lost on reload.</p>
        </div>}
        {template === 'builder' && <>
          {!currentAccess?.dynamicEnabled && <p className="deployment-notice">Dynamic apps require the operator to configure a managed database and separate visitor identity pool. Static pages and drafts still work.</p>}
          {currentAccess?.dynamicEnabled && !currentAccess.canUseDynamic && <p className="deployment-notice">Publishing dynamic apps requires the separate database add-on and an active Cookist plan.
            {!currentAccess.databaseBillingAvailable ? ' The operator must configure database billing before authorization is available.' :
              currentAccess.role === 'customer_admin' ? ' Manage your database billing above.' : ' Ask your organization administrator to authorize database billing.'}
            {' Existing data remains available for reading and deletion.'}</p>}
          <DynamicEditor key={`${editingId ?? 'new-design'}-${editorRevision}`} value={dynamic} onChange={setDynamic} disabled={submitting || draftBusy || !currentAccess?.canUseDynamic} editing={!!editingId} />
        </>}
        {template === 'builder' ? <SiteEditor key={`${editingId ?? 'new-design'}-${editorRevision}`} blocks={blocks} onChange={setBlocks} style={siteStyle} disabled={submitting || draftBusy} catalog={catalogPreview} dynamic={dynamic} /> : <div className="site-preview-block">
          <h4>Site preview</h4>
          <div className={`site-preview generated-site site-style-${siteStyle}`} role="region" aria-label="Site preview">
            <div className="site-content">
              <h4 className="site-title">{template !== 'blank' ? businessName.trim() || (template === 'storefront' ? 'Your store' : 'Your business') : name.trim() || 'Your site'}</h4>
              {template === 'storefront' ? catalogPreview : <>
                <h5 className="site-list-title">{template === 'business' ? listTitle.trim() || 'Your offerings' : 'Your next big idea'}</h5>
                <ul>{(template === 'business' && previewItems.length ? previewItems : ['Your first offering', 'Something your customers will love']).map((item, index) =>
                  <li key={index}>{item}</li>)}</ul>
              </>}
            </div>
          </div>
          <p className="site-preview-note">{template === 'blank'
            ? 'Sample content previews the style only. Your blank site stays empty.'
            : template === 'storefront' ? "Buy buttons are disabled in this preview. Your deployed site opens each product's Stripe checkout."
            : 'Your business details update this preview. Empty lists show sample items here only.'}</p>
        </div>}
        <p className="deployment-notice">Deploying creates a public static site on AWS S3 and CloudFront in the configured AWS account.
          Anyone with the site URL can access it; it is not Tailscale-private.
          AWS charges may apply until the resources are destroyed. Business listings are static lists; storefronts link to seller-owned Stripe checkout.
          Cookist does not store card details or process storefront orders.</p>
        <div className="form-actions"><button className="primary" type="submit" disabled={submitting || draftBusy || !authenticatedSession}>{submitting ? 'Starting...' : editingId ? 'Publish changes' : 'Deploy site'}</button>
          <button type="button" className="secondary" disabled={submitting} onClick={() => { setCreating(false); setMessage(null); newDeploymentButton.current?.focus(); }}>Cancel</button></div>
        {message && <p role={message.error ? 'alert' : 'status'}>{message.text}</p>}
      </form>}
    </section>
  </main>;
}
