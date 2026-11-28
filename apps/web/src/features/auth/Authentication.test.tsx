import { StrictMode } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Authentication } from './Authentication';

const mocks = vi.hoisted(() => {
  const events = {
    addUserLoaded: vi.fn(), addUserUnloaded: vi.fn(), addAccessTokenExpired: vi.fn(), addSilentRenewError: vi.fn(),
    removeUserLoaded: vi.fn(), removeUserUnloaded: vi.fn(), removeAccessTokenExpired: vi.fn(), removeSilentRenewError: vi.fn(),
  };
  return {
    getUser: vi.fn(), signinRedirect: vi.fn(), signinRedirectCallback: vi.fn(),
    revokeTokens: vi.fn(), removeUser: vi.fn(), startSilentRenew: vi.fn(), stopSilentRenew: vi.fn(), events, api: vi.fn(),
  };
});

vi.mock('oidc-client-ts', () => ({
  UserManager: class {
    getUser = mocks.getUser;
    signinRedirect = mocks.signinRedirect;
    signinRedirectCallback = mocks.signinRedirectCallback;
    revokeTokens = mocks.revokeTokens;
    removeUser = mocks.removeUser;
    startSilentRenew = mocks.startSilentRenew;
    stopSilentRenew = mocks.stopSilentRenew;
    events = mocks.events;
  },
  WebStorageStateStore: class {},
}));
vi.mock('../../shared/api', () => ({
  api: mocks.api,
  errorMessage: (error: unknown) => error instanceof Error ? error.message : 'Unexpected error',
}));
vi.mock('../../app/App', () => ({
  App: ({ authenticatedSession, onSignOut, authError }: {
    authenticatedSession?: { actorId: string }; onSignOut?: () => void; authError?: string;
  }) => <div>
    {authenticatedSession ? `Signed in: ${authenticatedSession.actorId}` : 'Workspace'}
    {onSignOut && <button onClick={onSignOut}>Sign out</button>}
    {authError && <p role="alert">{authError}</p>}
  </div>,
}));

beforeEach(() => {
  vi.stubEnv('VITE_AUTH_MODE', 'cognito');
  vi.stubEnv('VITE_COGNITO_ISSUER', 'https://cognito-idp.us-west-2.amazonaws.com/us-west-2_Test');
  vi.stubEnv('VITE_COGNITO_CLIENT_ID', 'client123');
  vi.stubEnv('VITE_COGNITO_DOMAIN', 'https://cookist.auth.us-west-2.amazoncognito.com');
  mocks.getUser.mockResolvedValue(null);
  mocks.signinRedirect.mockResolvedValue(undefined);
  mocks.api.mockResolvedValue({ actorId: 'user-1' });
});

afterEach(() => {
  cleanup();
  vi.unstubAllEnvs();
  vi.clearAllMocks();
  sessionStorage.clear();
  window.history.replaceState(null, '', '/');
});

describe('Cognito authentication', () => {
  it('gates the app and redirects to sign in or sign up', async () => {
    render(<Authentication />);
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in or sign up' }));
    expect(mocks.signinRedirect).toHaveBeenCalledOnce();
    expect(screen.queryByText('Workspace')).not.toBeInTheDocument();
  });

  it('opens the app only after the backend validates the access token', async () => {
    mocks.getUser.mockResolvedValue({ expired: false, access_token: 'access-token' });
    render(<Authentication />);
    expect(await screen.findByText('Signed in: user-1')).toBeInTheDocument();
    expect(mocks.api).toHaveBeenCalledWith('/api/me', expect.objectContaining({ token: 'access-token' }));
  });

  it('keeps the app closed if the backend rejects the token', async () => {
    mocks.getUser.mockResolvedValue({ expired: false, access_token: 'invalid' });
    mocks.api.mockRejectedValue(new Error('Invalid access token'));
    render(<Authentication />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Invalid access token');
    expect(screen.queryByText(/Signed in:/)).not.toBeInTheDocument();
  });

  it('surfaces sign-out failures instead of claiming success', async () => {
    mocks.getUser.mockResolvedValue({ expired: false, access_token: 'access-token' });
    mocks.revokeTokens.mockRejectedValue(new Error('network unavailable'));
    render(<Authentication />);
    fireEvent.click(await screen.findByRole('button', { name: 'Sign out' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not sign out'));
    expect(mocks.removeUser).not.toHaveBeenCalled();
  });

  it('does not silently fall back to demo when configuration is incomplete', () => {
    vi.stubEnv('VITE_COGNITO_CLIENT_ID', '');
    render(<Authentication />);
    expect(screen.getByRole('alert')).toHaveTextContent('Invalid Cognito configuration');
    expect(screen.queryByText('Workspace')).not.toBeInTheDocument();
  });

  it('handles the OAuth callback once under React strict mode', async () => {
    window.history.replaceState(null, '', '/auth/callback?code=example&state=example');
    mocks.signinRedirectCallback.mockResolvedValue({ expired: false, access_token: 'access-token' });
    render(<StrictMode><Authentication /></StrictMode>);
    expect(await screen.findByText('Signed in: user-1')).toBeInTheDocument();
    expect(mocks.signinRedirectCallback).toHaveBeenCalledOnce();
    expect(window.location.pathname).toBe('/');
  });
});
