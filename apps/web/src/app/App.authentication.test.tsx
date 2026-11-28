import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from './App';

afterEach(() => cleanup());

it('provides sign-out for an authenticated account', () => {
  const onSignOut = vi.fn();
  render(<App authenticatedSession={{ token: 'access-token', actorId: 'user-1' }} onSignOut={onSignOut} />);
  fireEvent.click(screen.getByRole('button', { name: 'Sign out' }));
  expect(onSignOut).toHaveBeenCalledOnce();
});
