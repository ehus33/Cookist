import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CourierLocationControls } from './CourierLocationControls';
import { ApiError } from './api';
import type { Order, Session } from './types';

const mocks = vi.hoisted(() => ({ api: vi.fn() }));
vi.mock('./api', async importOriginal => ({
  ...await importOriginal<typeof import('./api')>(),
  api: mocks.api,
}));

const session: Session = { token: 'verified-token', role: 'courier', actorId: 'courier-one' };
const order = { id: 'order-one' } as Order;
const position: GeolocationPosition = {
  coords: { latitude: 47.6, longitude: -122.3, accuracy: 10, altitude: null, altitudeAccuracy: null, heading: null, speed: null, toJSON: () => ({}) },
  timestamp: Date.now(),
  toJSON: () => ({}),
};
const getPosition = vi.fn<(success: PositionCallback, error?: PositionErrorCallback) => void>();

beforeEach(() => {
  mocks.api.mockReset();
  getPosition.mockReset();
  vi.stubGlobal('isSecureContext', true);
  vi.stubGlobal('navigator', { geolocation: { getCurrentPosition: getPosition } });
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe('courier location consent', () => {
  it('does not request device location in demo mode', async () => {
    mocks.api.mockRejectedValue(new ApiError('Not found', 404));
    render(<CourierLocationControls session={session} order={order} busy={false} onLocation={vi.fn()} />);
    expect(await screen.findByText('MANUAL LOCATION SIMULATION')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Share live location' })).not.toBeInTheDocument();
    expect(getPosition).not.toHaveBeenCalled();
  });

  it('requests permission only after consent and stops sharing when the page is hidden', async () => {
    mocks.api.mockImplementation(async (path: string) => {
      if (path === '/api/me') return { role: 'courier', actorId: session.actorId };
      if (path.endsWith('/location-start')) return { sharingId: 'share-123' };
      return { status: 'ok' };
    });
    getPosition.mockImplementation(success => success(position));
    render(<CourierLocationControls session={session} order={order} busy={false} onLocation={vi.fn()} />);
    const button = await screen.findByRole('button', { name: 'Share live location' });
    expect(getPosition).not.toHaveBeenCalled();
    fireEvent.click(button);
    await waitFor(() => expect(mocks.api).toHaveBeenCalledWith('/api/orders/order-one/location', session, {
      method: 'POST', body: { lat: 47.6, lng: -122.3, sharingId: 'share-123' },
    }));
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    act(() => document.dispatchEvent(new Event('visibilitychange')));
    await waitFor(() => expect(mocks.api).toHaveBeenCalledWith('/api/orders/order-one/location-stop', session, {
      method: 'POST', body: { sharingId: 'share-123' },
    }));
    expect(screen.getByText('Location sharing stopped because this page is no longer visible.')).toBeInTheDocument();
  });

  it('does not start a server session when permission is denied', async () => {
    mocks.api.mockResolvedValue({ role: 'courier', actorId: session.actorId });
    getPosition.mockImplementation((_success, error) => error?.({ code: 1, message: 'denied', PERMISSION_DENIED: 1, POSITION_UNAVAILABLE: 2, TIMEOUT: 3 }));
    render(<CourierLocationControls session={session} order={order} busy={false} onLocation={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Share live location' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Location permission was denied');
    expect(mocks.api.mock.calls.some(call => call[0].endsWith('/location-start'))).toBe(false);
  });

  it('ends a late start without uploading a position after the courier stops', async () => {
    let resolveStart: (value: { sharingId: string }) => void = () => { throw new Error('Start was not requested'); };
    mocks.api.mockImplementation((path: string) => {
      if (path === '/api/me') return Promise.resolve({ role: 'courier', actorId: session.actorId });
      if (path.endsWith('/location-start')) return new Promise(resolve => { resolveStart = resolve; });
      return Promise.resolve({ status: 'ok' });
    });
    getPosition.mockImplementation(success => success(position));
    render(<CourierLocationControls session={session} order={order} busy={false} onLocation={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Share live location' }));
    await waitFor(() => expect(mocks.api.mock.calls.some(call => call[0].endsWith('/location-start'))).toBe(true));
    fireEvent.click(screen.getByRole('button', { name: 'Stop sharing location' }));
    await act(async () => { resolveStart({ sharingId: 'late-id' }); });
    await waitFor(() => expect(mocks.api).toHaveBeenCalledWith('/api/orders/order-one/location-stop', session, {
      method: 'POST', body: { sharingId: 'late-id' },
    }));
    expect(mocks.api.mock.calls.some(call => call[0].endsWith('/location'))).toBe(false);
  });
});
