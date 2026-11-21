import { useEffect, useState } from 'react';
import { UserManager, WebStorageStateStore } from 'oidc-client-ts';
import type { User } from 'oidc-client-ts';
import { App } from './App';
import { api, errorMessage } from './api';
import { isSession } from './domain';
import type { Session } from './types';

type Settings = { issuer: string; clientId: string; domain: string };
let callback: Promise<User> | undefined;

function settings(): Settings | null {
  const { VITE_AUTH_MODE, VITE_COGNITO_ISSUER, VITE_COGNITO_CLIENT_ID, VITE_COGNITO_DOMAIN } = import.meta.env;
  if (!VITE_AUTH_MODE || VITE_AUTH_MODE === 'demo') {
    if (VITE_COGNITO_ISSUER || VITE_COGNITO_CLIENT_ID || VITE_COGNITO_DOMAIN) {
      throw new Error('Cognito configuration requires VITE_AUTH_MODE=cognito.');
    }
    return null;
  }
  if (VITE_AUTH_MODE !== 'cognito'
    || !/^https:\/\/cognito-idp\.[a-z0-9-]+\.amazonaws\.com\/[a-z0-9-]+_[A-Za-z0-9]+$/.test(VITE_COGNITO_ISSUER ?? '')
    || !/^[A-Za-z0-9]+$/.test(VITE_COGNITO_CLIENT_ID ?? '')
    || !/^https:\/\/[a-z0-9-]+\.auth\.[a-z0-9-]+\.amazoncognito\.com$/.test(VITE_COGNITO_DOMAIN ?? '')) {
    throw new Error('Invalid Cognito configuration. Contact the administrator.');
  }
  return { issuer: VITE_COGNITO_ISSUER, clientId: VITE_COGNITO_CLIENT_ID, domain: VITE_COGNITO_DOMAIN };
}

function makeManager(config: Settings) {
  return new UserManager({
    authority: config.issuer,
    client_id: config.clientId,
    redirect_uri: `${location.origin}/auth/callback`,
    response_type: 'code',
    scope: 'openid email',
    automaticSilentRenew: false,
    userStore: new WebStorageStateStore({ store: sessionStorage }),
    stateStore: new WebStorageStateStore({ store: sessionStorage }),
  });
}

async function checkedSession(user: User): Promise<Session> {
  if (user.expired || !user.access_token) throw new Error('Your session expired. Please sign in again.');
  const identity = await api<unknown>('/api/me', { token: user.access_token, actorId: '' });
  if (!identity || typeof identity !== 'object') throw new Error('The server returned an invalid account.');
  const session: unknown = { ...identity, token: user.access_token };
  if (!isSession(session) || !session.actorId) throw new Error('The server returned an invalid account.');
  return session;
}

export function Authentication() {
  const [config] = useState(() => {
    try { return { value: settings(), error: '' }; }
    catch (error) { return { value: null, error: errorMessage(error) }; }
  });
  const [manager] = useState(() => config.value ? makeManager(config.value) : null);
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(!!manager);
  const [error, setError] = useState(config.error);
  const [startup] = useState(() => manager && (location.pathname === '/auth/callback'
    ? (callback ??= manager.signinRedirectCallback().finally(() => {
      history.replaceState(null, '', '/');
    }))
    : manager.getUser()));

  useEffect(() => {
    if (!manager || !startup) return;
    let active = true;
    let generation = 0;
    const accept = async (user: User | null) => {
      const current = ++generation;
      try {
        const next = user ? await checkedSession(user) : null;
        if (active && current === generation) { setSession(next); setError(''); }
      } catch (cause) {
        if (active && current === generation) { setSession(null); setError(errorMessage(cause)); }
      } finally {
        if (active && current === generation) setLoading(false);
      }
    };
    const onUser = (user: User) => { void accept(user); };
    const onExpired = () => { void accept(null); };
    const onRenewError = (cause: Error) => {
      if (active) {
        generation++;
        setSession(null); setLoading(false); setError(`Session renewal failed: ${errorMessage(cause)}`);
      }
    };
    manager.events.addUserLoaded(onUser);
    manager.events.addUserUnloaded(onExpired);
    manager.events.addAccessTokenExpired(onExpired);
    manager.events.addSilentRenewError(onRenewError);
    startup.then(user => {
      if (active) { manager.startSilentRenew(); void accept(user); }
    })
      .catch(cause => { if (active) { setError(errorMessage(cause)); setLoading(false); } });
    return () => {
      active = false;
      manager.events.removeUserLoaded(onUser);
      manager.events.removeUserUnloaded(onExpired);
      manager.events.removeAccessTokenExpired(onExpired);
      manager.events.removeSilentRenewError(onRenewError);
      manager.stopSilentRenew();
    };
  }, [manager, startup]);

  async function signOut() {
    if (!manager || !config.value) return;
    try {
      await manager.revokeTokens(['refresh_token']);
      await manager.removeUser();
      const url = new URL(`${config.value.domain}/logout`);
      url.searchParams.set('client_id', config.value.clientId);
      url.searchParams.set('logout_uri', `${location.origin}/`);
      location.assign(url.href);
    } catch (cause) { setError(`Could not sign out: ${errorMessage(cause)}`); }
  }

  if (config.error) return <main className="auth-page"><h1>Cookist unavailable</h1><p role="alert">{config.error}</p></main>;
  if (!manager) return <App />;
  if (loading) return <main className="auth-page"><p role="status">Checking your account...</p></main>;
  if (!session) return <main className="auth-page"><h1>Welcome to Cookist</h1>
    <p>Sign in or create an account to continue.</p>
    {error && <p role="alert">{error}</p>}
    <button className="primary" type="button" onClick={() => void manager.signinRedirect().catch(cause => setError(errorMessage(cause)))}>
      Sign in or sign up
    </button>
  </main>;
  return <App authenticatedSession={session} onSignOut={() => void signOut()} authError={error} />;
}
