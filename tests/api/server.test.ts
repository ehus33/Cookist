import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ listen: vi.fn(), createApp: vi.fn(), createDatabase: vi.fn() }));
vi.mock('../../src/app.js', () => ({ createApp: mocks.createApp }));
vi.mock('../../src/db.js', () => ({ createDatabase: mocks.createDatabase }));

beforeEach(() => {
  vi.resetModules();
  vi.stubEnv('STRIPE_SECRET_KEY', 'sk_test_sample');
  vi.stubEnv('STRIPE_WEBHOOK_SECRET', 'whsec_sample');
  vi.stubEnv('ADMIN_TOKEN', 'a-long-enough-admin-secret-123');
  vi.stubEnv('APP_URL', 'http://localhost:3000');
  vi.stubEnv('DATABASE_PATH', ':memory:');
  vi.stubEnv('PORT', '3000');
  mocks.createApp.mockReturnValue({ listen: mocks.listen });
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe('startup configuration', () => {
  it.each([
    ['STRIPE_SECRET_KEY', 'sk_live_invalid'], ['STRIPE_SECRET_KEY', ''],
    ['STRIPE_WEBHOOK_SECRET', 'bad'], ['ADMIN_TOKEN', 'short'],
    ['ADMIN_TOKEN', ''], ['ADMIN_TOKEN', 'replace-with-a-long-random-secret']
  ])('rejects invalid %s', async (key, value) => {
    vi.stubEnv(key, value);
    await expect(import('../../src/server.js')).rejects.toThrow('Set a Stripe test secret key');
    expect(mocks.listen).not.toHaveBeenCalled();
  });

  it.each(['ftp://localhost', 'http://user:pass@localhost', 'http://localhost/path',
    'http://localhost/?query=1', 'http://localhost/#fragment'])('rejects invalid APP_URL %s', async url => {
    vi.stubEnv('APP_URL', url);
    await expect(import('../../src/server.js')).rejects.toThrow('APP_URL must be an HTTP(S) origin');
    expect(mocks.listen).not.toHaveBeenCalled();
  });

  it('starts on loopback with the configured database and origin', async () => {
    vi.spyOn(console, 'log').mockImplementation(() => {});
    mocks.listen.mockImplementation((_port, _host, callback) => callback());
    await import('../../src/server.js');
    expect(mocks.createDatabase).toHaveBeenCalledWith(':memory:');
    expect(mocks.createApp).toHaveBeenCalledWith(expect.objectContaining({
      adminToken: 'a-long-enough-admin-secret-123', appUrl: 'http://localhost:3000', webhookSecret: 'whsec_sample'
    }));
    expect(mocks.listen).toHaveBeenCalledWith(3000, '127.0.0.1', expect.any(Function));
  });
});
