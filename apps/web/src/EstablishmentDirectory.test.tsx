import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { EstablishmentDirectory } from './EstablishmentDirectory';

const mocks = vi.hoisted(() => ({
  maps: [] as {
    on: ReturnType<typeof vi.fn>;
    addSource: ReturnType<typeof vi.fn>;
    getSource: ReturnType<typeof vi.fn>;
    remove: ReturnType<typeof vi.fn>;
    easeTo: ReturnType<typeof vi.fn>;
    source: { setData: ReturnType<typeof vi.fn>; getClusterExpansionZoom: ReturnType<typeof vi.fn> };
  }[],
  markers: [] as { element: HTMLElement; setLngLat: ReturnType<typeof vi.fn>; remove: ReturnType<typeof vi.fn> }[],
}));

vi.mock('maplibre-gl', () => ({
  setWorkerUrl: vi.fn(),
  Map: class {
    on = vi.fn();
    addControl = vi.fn();
    addLayer = vi.fn();
    addSource = vi.fn();
    source = { setData: vi.fn(), getClusterExpansionZoom: vi.fn().mockResolvedValue(12) };
    getSource = vi.fn(() => this.source);
    remove = vi.fn();
    easeTo = vi.fn();
    constructor() { mocks.maps.push(this); }
  },
  Marker: class {
    element: HTMLElement;
    setLngLat = vi.fn().mockReturnThis();
    addTo = vi.fn().mockReturnThis();
    remove = vi.fn();
    constructor({ element }: { element: HTMLElement }) { this.element = element; mocks.markers.push(this); }
  },
  NavigationControl: class {},
  AttributionControl: class {},
}));

const listings = {
  type: 'FeatureCollection', features: [
    { type: 'Feature', geometry: { type: 'Point', coordinates: [-122.3, 47.6] }, properties: { name: '<img src=x onerror=alert(1)>', address: '1 Main', city: 'Seattle', zipcode: '98101' } },
    { type: 'Feature', geometry: { type: 'Point', coordinates: [-122.2, 47.7] }, properties: { name: 'Portage Bay Cafe', address: '2 Main', city: 'Seattle', zipcode: '98102', website: 'https://portagebaycafe.com/' } },
  ],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  mocks.maps.length = 0;
  mocks.markers.length = 0;
});

describe('establishment directory', () => {
  it('loads on demand, clusters listings, filters them and renders selected details as text', async () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => listings });
    vi.stubGlobal('fetch', fetch);
    render(<EstablishmentDirectory />);
    expect(fetch).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    expect(await screen.findByText(/2 listings match/)).toBeInTheDocument();
    await waitFor(() => expect(mocks.maps).toHaveLength(1));
    const map = mocks.maps[0];
    const load = map.on.mock.calls.find(call => call[0] === 'load')?.[1];
    act(() => load());
    expect(map.addSource).toHaveBeenCalledWith('establishments', expect.objectContaining({ cluster: true, data: listings }));

    fireEvent.change(screen.getByLabelText('Search directory by name, address, city, or ZIP'), { target: { value: 'portage' } });
    expect(screen.getByText(/1 listings match/)).toBeInTheDocument();
    expect(map.source.setData).toHaveBeenLastCalledWith(expect.objectContaining({ features: [listings.features[1]] }));
    fireEvent.click(screen.getByRole('button', { name: /Portage Bay Cafe/ }));
    expect(map.easeTo).toHaveBeenCalledWith({ center: [-122.2, 47.7], zoom: 16 });
    expect(screen.getByText('Directory listing only; not available to order.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Visit unverified website' })).toHaveAttribute('href', 'https://portagebaycafe.com/');
    expect(screen.getByRole('link', { name: 'Visit unverified website' })).toHaveAttribute('rel', 'noopener noreferrer');

    fireEvent.change(screen.getByLabelText('Search directory by name, address, city, or ZIP'), { target: { value: '<img' } });
    fireEvent.click(screen.getByRole('button', { name: /<img src=x/ }));
    expect(screen.getAllByText('<img src=x onerror=alert(1)>')).toHaveLength(2);
    expect(document.querySelector('img')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Hide directory map' }));
    expect(map.remove).toHaveBeenCalled();
  });

  it('reports invalid directory data and retries', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: async () => ({}) })
      .mockResolvedValueOnce({ ok: true, json: async () => listings });
    vi.stubGlobal('fetch', fetch);
    render(<EstablishmentDirectory />);
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('could not be loaded');
    fireEvent.click(screen.getByRole('button', { name: 'Retry directory' }));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect(await screen.findByText(/2 listings match/)).toBeInTheDocument();
  });

  it('expands clusters and displays clicked listings', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => listings }));
    render(<EstablishmentDirectory />);
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    await screen.findByText(/2 listings match/);
    await waitFor(() => expect(mocks.maps).toHaveLength(1));
    const map = mocks.maps[0];
    act(() => map.on.mock.calls.find(call => call[0] === 'load')?.[1]());
    const clusterClick = map.on.mock.calls.find(call => call[0] === 'click' && call[1] === 'establishment-clusters')?.[2];
    await act(async () => clusterClick({ features: [{ geometry: { type: 'Point', coordinates: [-122.3, 47.6] }, properties: { cluster_id: 7 } }] }));
    expect(map.source.getClusterExpansionZoom).toHaveBeenCalledWith(7);
    expect(map.easeTo).toHaveBeenCalledWith({ center: [-122.3, 47.6], zoom: 12 });
    const pointClick = map.on.mock.calls.find(call => call[0] === 'click' && call[1] === 'establishment-points')?.[2];
    act(() => pointClick({ features: [listings.features[1]] }));
    expect(screen.getByText('Portage Bay Cafe')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Visit unverified website' })).toHaveAttribute('href', 'https://portagebaycafe.com/');
  });

  it('does not render unsafe websites or search website fields', async () => {
    const unsafe = { ...listings, features: [{ ...listings.features[1], properties: {
      ...listings.features[1].properties, website: 'javascript:alert(1)', websiteSourceId: 'distinctsourceid',
    } }] };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => unsafe }));
    render(<EstablishmentDirectory />);
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    await screen.findByText(/1 listings match/);
    const search = screen.getByLabelText('Search directory by name, address, city, or ZIP');
    fireEvent.change(search, { target: { value: 'distinctsourceid' } });
    expect(screen.getByText(/0 listings match/)).toBeInTheDocument();
    fireEvent.change(search, { target: { value: 'portage' } });
    fireEvent.click(screen.getByRole('button', { name: /Portage Bay Cafe/ }));
    expect(screen.queryByRole('link', { name: 'Visit unverified website' })).not.toBeInTheDocument();
  });

  it('requests location on click, updates the marker, and stops on demand', async () => {
    const watchPosition = vi.fn().mockReturnValue(42);
    const clearWatch = vi.fn();
    vi.stubGlobal('navigator', { geolocation: { watchPosition, clearWatch } });
    vi.stubGlobal('isSecureContext', true);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => listings }));
    render(<EstablishmentDirectory />);
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    await screen.findByText(/2 listings match/);
    await waitFor(() => expect(mocks.maps).toHaveLength(1));
    expect(watchPosition).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Use my location' }));
    expect(watchPosition).toHaveBeenCalledOnce();
    expect(screen.getByText(/Waiting for location permission/)).toBeInTheDocument();
    const success = watchPosition.mock.calls[0][0];
    act(() => success({ coords: { latitude: 47.6, longitude: -122.3 } }));
    expect(mocks.maps[0].easeTo).toHaveBeenCalledWith({ center: [-122.3, 47.6], zoom: 14 });
    expect(mocks.markers[0].element).toHaveAttribute('aria-label', 'Your current location');
    expect(mocks.markers[0].setLngLat).toHaveBeenCalledWith([-122.3, 47.6]);
    act(() => success({ coords: { latitude: 47.61, longitude: -122.31 } }));
    expect(mocks.markers[0].remove).toHaveBeenCalled();
    expect(mocks.markers[1].setLngLat).toHaveBeenCalledWith([-122.31, 47.61]);
    expect(mocks.maps[0].easeTo).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: 'Center on my location' }));
    expect(mocks.maps[0].easeTo).toHaveBeenLastCalledWith({ center: [-122.31, 47.61], zoom: 14 });
    fireEvent.click(screen.getByRole('button', { name: 'Stop using location' }));
    expect(clearWatch).toHaveBeenCalledWith(42);
    expect(mocks.markers[1].remove).toHaveBeenCalled();
    act(() => success({ coords: { latitude: 47.62, longitude: -122.32 } }));
    expect(mocks.markers).toHaveLength(2);
  });

  it('reports permission denial and clears the location watch', async () => {
    const watchPosition = vi.fn().mockReturnValue(7);
    const clearWatch = vi.fn();
    vi.stubGlobal('navigator', { geolocation: { watchPosition, clearWatch } });
    vi.stubGlobal('isSecureContext', true);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => listings }));
    render(<EstablishmentDirectory />);
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    await screen.findByText(/2 listings match/);
    await waitFor(() => expect(mocks.maps).toHaveLength(1));
    fireEvent.click(screen.getByRole('button', { name: 'Use my location' }));
    act(() => watchPosition.mock.calls[0][0]({ coords: { latitude: 47.6, longitude: -122.3 } }));
    act(() => watchPosition.mock.calls[0][1]({ code: 1 }));
    expect(screen.getByRole('alert')).toHaveTextContent('Location permission was denied');
    expect(clearWatch).toHaveBeenCalledWith(7);
    expect(mocks.markers[0].remove).toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Use my location' })).toBeInTheDocument();
  });

  it('stops location when the page becomes hidden and when the map closes', async () => {
    const watchPosition = vi.fn().mockReturnValue(9);
    const clearWatch = vi.fn();
    vi.stubGlobal('navigator', { geolocation: { watchPosition, clearWatch } });
    vi.stubGlobal('isSecureContext', true);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => listings }));
    render(<EstablishmentDirectory />);
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    await screen.findByText(/2 listings match/);
    await waitFor(() => expect(mocks.maps).toHaveLength(1));
    fireEvent.click(screen.getByRole('button', { name: 'Use my location' }));
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    act(() => document.dispatchEvent(new Event('visibilitychange')));
    expect(clearWatch).toHaveBeenCalledWith(9);
    expect(screen.getByRole('alert')).toHaveTextContent('page is no longer visible');
    visibility.mockReturnValue('visible');
    fireEvent.click(screen.getByRole('button', { name: 'Use my location' }));
    fireEvent.click(screen.getByRole('button', { name: 'Hide directory map' }));
    expect(clearWatch).toHaveBeenCalledTimes(2);
  });

  it('explains when location is unavailable on an insecure connection', async () => {
    const watchPosition = vi.fn();
    vi.stubGlobal('navigator', { geolocation: { watchPosition, clearWatch: vi.fn() } });
    vi.stubGlobal('isSecureContext', false);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => listings }));
    render(<EstablishmentDirectory />);
    fireEvent.click(screen.getByRole('button', { name: 'Explore directory map' }));
    await screen.findByText(/2 listings match/);
    fireEvent.click(screen.getByRole('button', { name: 'Use my location' }));
    expect(screen.getByRole('alert')).toHaveTextContent('requires a secure connection');
    expect(watchPosition).not.toHaveBeenCalled();
  });
});
