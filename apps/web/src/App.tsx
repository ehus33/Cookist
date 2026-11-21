import type { Session } from './types';

export function App({ authenticatedSession, onSignOut, authError }: {
  authenticatedSession?: Session; onSignOut?: () => void; authError?: string;
} = {}) {
  return <main className="workspace">
    <header>
      <div>
        <h1>Cookist</h1>
        <p>A Machi service</p>
      </div>
      {authenticatedSession && <button type="button" onClick={onSignOut}>Sign out</button>}
    </header>
    {authError && <p className="error" role="alert">{authError}</p>}
    <section>
      <h2>Workspace</h2>
      <p>The workspace is being prepared.</p>
    </section>
  </main>;
}
