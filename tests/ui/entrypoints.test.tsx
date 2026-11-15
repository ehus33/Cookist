// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ createRoot: vi.fn(), render: vi.fn() }));
vi.mock('react-dom/client', () => ({ createRoot: mocks.createRoot }));

afterEach(() => {
  document.body.replaceChildren();
  vi.resetModules();
  vi.clearAllMocks();
});

describe('frontend entry points', () => {
  it.each([
    ['storefront', () => import('../../src/public/main.js')],
    ['admin', () => import('../../src/public/admin-entry.js')]
  ])('mounts the %s page', async (_name, load) => {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.append(root);
    mocks.createRoot.mockReturnValue({ render: mocks.render });
    await load();
    expect(mocks.createRoot).toHaveBeenCalledWith(root);
    expect(mocks.render).toHaveBeenCalledOnce();
  });

  it.each([
    ['storefront', () => import('../../src/public/main.js'), 'Storefront root element is missing.'],
    ['admin', () => import('../../src/public/admin-entry.js'), 'Admin root element is missing.']
  ])('reports a missing %s root', async (_name, load, message) => {
    await expect(load()).rejects.toThrow(message);
  });
});
