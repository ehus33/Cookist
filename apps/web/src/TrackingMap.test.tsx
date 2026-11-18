import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import catalog from '../../../shared/catalog.json';
import { TrackingMap } from './TrackingMap';

const mocks = vi.hoisted(() => ({
  maps: [] as { on: ReturnType<typeof vi.fn>; remove: ReturnType<typeof vi.fn>; fitBounds: ReturnType<typeof vi.fn> }[],
  markers: [] as { element: HTMLElement; offset: [number, number]; setLngLat: ReturnType<typeof vi.fn>; remove: ReturnType<typeof vi.fn> }[],
}));

vi.mock('maplibre-gl', () => ({
  setWorkerUrl: vi.fn(),
  Map: class {
    on = vi.fn();
    remove = vi.fn();
    fitBounds = vi.fn();
    jumpTo = vi.fn();
    addControl = vi.fn();
    constructor() { mocks.maps.push(this); }
  },
  Marker: class {
    element: HTMLElement;
    offset: [number, number];
    setLngLat = vi.fn().mockReturnThis();
    addTo = vi.fn().mockReturnThis();
    remove = vi.fn();
    constructor({ element, offset }: { element: HTMLElement; offset: [number, number] }) { this.element = element; this.offset = offset; mocks.markers.push(this); }
  },
  LngLatBounds: class { extend = vi.fn().mockReturnThis(); },
  NavigationControl: class {},
  AttributionControl: class {},
}));

afterEach(() => {
  cleanup();
  mocks.maps.length = 0;
  mocks.markers.length = 0;
  vi.restoreAllMocks();
});

describe('OpenFreeMap tracker', () => {
  it('only shows a courier after a simulated location exists and updates its coordinates', () => {
    const store = catalog[0];
    const result = render(<TrackingMap store={store} location={null} />);
    expect(mocks.markers).toHaveLength(1);
    expect(mocks.markers[0].element).toHaveAttribute('aria-label', 'Demo kitchen');
    expect(mocks.markers[0].setLngLat).toHaveBeenCalledWith([store.lng, store.lat]);

    const location = { lat: 47.6, lng: -122.3, updatedAt: '2026-01-01' };
    result.rerender(<TrackingMap store={store} location={location} />);
    expect(mocks.maps).toHaveLength(1);
    expect(mocks.markers[0].remove).toHaveBeenCalled();
    expect(mocks.markers[2].element).toHaveAttribute('aria-label', 'Simulated courier');
    expect(mocks.markers[2].setLngLat).toHaveBeenCalledWith([-122.3, 47.6]);
    expect(mocks.maps[0].fitBounds).toHaveBeenCalled();

    result.rerender(<TrackingMap store={store} location={{ ...location, lat: 90 }} />);
    expect(screen.getByText('The courier coordinates cannot be shown on the map.')).toBeInTheDocument();
    expect(mocks.markers[2].remove).toHaveBeenCalled();
  });

  it('keeps both markers visible when the courier is at the kitchen', () => {
    const store = catalog[0];
    render(<TrackingMap store={store} location={{ lat: store.lat, lng: store.lng, updatedAt: '2026-01-01' }} />);
    expect(mocks.markers[1].setLngLat).toHaveBeenCalledWith([store.lng, store.lat]);
    expect(mocks.markers[1].offset).toEqual([15, -15]);
  });

  it('reports map errors and retries without leaving the old map running', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    render(<TrackingMap store={catalog[0]} location={null} />);
    const errorHandler = mocks.maps[0].on.mock.calls.find(call => call[0] === 'error')?.[1];
    act(() => errorHandler({ error: new Error('offline') }));
    expect(screen.getByRole('alert')).toHaveTextContent('Some map data could not be loaded');
    fireEvent.click(screen.getByRole('button', { name: 'Retry map' }));
    expect(mocks.maps[0].remove).toHaveBeenCalled();
    expect(mocks.maps).toHaveLength(2);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
