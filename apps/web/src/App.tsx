import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, errorMessage } from './api';
import type { Session } from './types';

type DeploymentTemplate = 'blank' | 'business';
type Deployment = { id: string; name: string; region: string; status: 'running' | 'ready' | 'failed' | 'deleting' | 'cleanup_failed'; url?: string };
type DeploymentAccess = { role: string; subscribed: boolean; paymentRequired: boolean; canList: boolean; canCreate: boolean; canDelete: boolean };

function isDeploymentAccess(value: unknown): value is DeploymentAccess {
  if (!value || typeof value !== 'object') return false;
  const access = value as Record<string, unknown>;
  return typeof access.role === 'string'
    && ['platform_owner', 'customer_admin', 'deployer', 'viewer', 'none'].includes(access.role)
    && typeof access.subscribed === 'boolean' && typeof access.canList === 'boolean'
    && typeof access.canCreate === 'boolean' && typeof access.canDelete === 'boolean'
    && typeof access.paymentRequired === 'boolean';
}

function isDeployment(value: unknown): value is Deployment {
  if (!value || typeof value !== 'object') return false;
  const d = value as Record<string, unknown>;
  return typeof d.id === 'string' && typeof d.name === 'string' && typeof d.region === 'string'
    && (d.status === 'running' || d.status === 'ready' || d.status === 'failed' || d.status === 'deleting' || d.status === 'cleanup_failed')
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
  const [businessName, setBusinessName] = useState('');
  const [listTitle, setListTitle] = useState('Services');
  const [items, setItems] = useState('');
  const [message, setMessage] = useState<{ text: string; error: boolean } | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [loadError, setLoadError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Deployment | null>(null);
  const [access, setAccess] = useState<DeploymentAccess | null>(null);
  const [loadedActor, setLoadedActor] = useState<string | null>(null);
  const currentAccess = loadedActor === authenticatedSession?.actorId ? access : null;
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
          setLoadError('');
        }
      } catch (error) {
        if (!controller.signal.aborted && version === requestVersion.current) {
          setAccess(null);
          setDeployments([]);
          setCreating(false);
          setDeleteTarget(null);
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
    if (!authenticatedSession || !currentAccess?.canCreate) {
      setMessage({ text: 'Your account does not currently have permission to deploy.', error: true });
      return;
    }
    setSubmitting(true);
    setMessage(null);
    const version = sessionVersion.current;
    try {
      const result = await api<unknown>('/api/deployments', authenticatedSession, {
        method: 'POST', body: { name, region, template, businessName, listTitle, items },
      });
      if (version !== sessionVersion.current) return;
      if (!isDeployment(result)) throw new Error('The server returned an invalid deployment.');
      requestVersion.current++;
      setDeployments(previous => [result, ...previous.filter(item => item.id !== result.id)]);
      setCreating(false);
      setLoadError('');
      setMessage({ text: 'Deployment started. It may take several minutes.', error: false });
    } catch (error) {
      if (version === sessionVersion.current) setMessage({ text: `Could not confirm deployment: ${errorMessage(error)} Check the deployment list before trying again.`, error: true });
    } finally {
      if (version === sessionVersion.current) setSubmitting(false);
    }
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

  return <main className="workspace">
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
          onClick={() => { setCreating(true); setMessage(null); }}>
          <span aria-hidden="true">+</span> New deployment
        </button>
      </div>
      {!authenticatedSession && <p className="deployment-notice">Managed deployments require signing in with an authorized account.</p>}
      {authenticatedSession && !currentAccess && !loadError && <p role="status">Checking deployment access...</p>}
      {currentAccess && !currentAccess.canList && <section aria-label="Access required">
        <h3>{currentAccess.paymentRequired ? 'Paid access required' : 'Access required'}</h3>
        <p>Your account has not been granted access. Paid access is not available yet; contact the operator.</p>
      </section>}
      {currentAccess?.canList && currentAccess.paymentRequired && <section aria-label="Payment required">
        <h3>Paid access required</h3>
        <p className="deployment-notice">An active subscription is required to use deployment features.
          Existing deployment records remain available for recovery and, if your role permits,
          deletion to stop further usage charges. Checkout is not available yet; contact the operator.</p>
      </section>}
      {currentAccess?.role === 'viewer' && <p className="deployment-notice">Your role provides read-only deployment access.</p>}
      {loadError && <p role="alert">{loadError}</p>}
      {visibleDeployments.length > 0 && <ul className="deployment-list">
        {visibleDeployments.map(deployment => <li key={deployment.id}>
          <strong>{deployment.name}</strong> <span>{deployment.region} &mdash; {deployment.status}</span>
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
      {deleteTarget && currentAccess?.canDelete && <section role="region" aria-label={`Confirm deletion of ${deleteTarget.name}`}>
        <h3>Delete {deleteTarget.name}?</h3>
        <p>This permanently deletes this deployment's AWS resources and site content. After successful cleanup,
          its saved state is removed and its deployment slot is freed. Previously incurred AWS charges are not refunded.</p>
        <button type="button" className="primary" disabled={!!deleting} onClick={() => void deleteDeployment(deleteTarget)}>
          {deleting ? 'Starting cleanup...' : 'Confirm delete'}
        </button>
        <button type="button" disabled={!!deleting} onClick={() => setDeleteTarget(null)}>Cancel deletion</button>
      </section>}
      {!creating && visibleDeployments.length === 0 && (!authenticatedSession || currentAccess?.canList) && <div className="empty-state">
        <h3>Start with a blank slate</h3>
        <p>Or choose a business template for a menu, services, or another list.</p>
      </div>}
      {!creating && message && <p role={message.error ? 'alert' : 'status'}>{message.text}</p>}
      {creating && (!authenticatedSession || currentAccess?.canCreate) && <form className="deployment-form" onSubmit={event => void deploy(event)}>
        <h3>Create a Terraform deployment</h3>
        <label>Deployment name<input required maxLength={80} value={name} onChange={event => setName(event.target.value)} /></label>
        <label>AWS region
          <select required value={region} onChange={event => setRegion(event.target.value)}>
            {awsRegions.map(([code, location]) => <option key={code} value={code}>{location} ({code})</option>)}
          </select>
        </label>
        <p>Some regions must be enabled in your AWS account. CloudFront serves the site globally.</p>
        <fieldset>
          <legend>Choose a template</legend>
          <label className="template-choice">
            <input type="radio" name="template" checked={template === 'blank'} onChange={() => setTemplate('blank')} />
            <span><strong>Blank slate</strong><small>Start with an empty site.</small></span>
          </label>
          <label className="template-choice">
            <input type="radio" name="template" checked={template === 'business'} onChange={() => setTemplate('business')} />
            <span><strong>Business listing</strong><small>A simple menu, service list, or other offering.</small></span>
          </label>
        </fieldset>
        {template === 'business' && <div className="business-fields">
          <label>Business name<input required maxLength={80} value={businessName} onChange={event => setBusinessName(event.target.value)} /></label>
          <label>List title<input required maxLength={80} value={listTitle} onChange={event => setListTitle(event.target.value)} /></label>
          <label>Items, one per line<textarea maxLength={2000} rows={4} value={items} onChange={event => setItems(event.target.value)} /></label>
        </div>}
        <p className="deployment-notice">Deploying creates a public static site on AWS S3 and CloudFront in the configured AWS account.
          Anyone with the site URL can access it; it is not Tailscale-private.
          AWS charges may apply until the resources are destroyed. The business template is a static list, not ordering or payments.</p>
        <div className="form-actions"><button className="primary" type="submit" disabled={submitting || !authenticatedSession}>{submitting ? 'Starting...' : 'Deploy site'}</button>
          <button type="button" className="secondary" disabled={submitting} onClick={() => { setCreating(false); setMessage(null); newDeploymentButton.current?.focus(); }}>Cancel</button></div>
        {message && <p role={message.error ? 'alert' : 'status'}>{message.text}</p>}
      </form>}
    </section>
  </main>;
}
