import 'dotenv/config';
import Stripe from 'stripe';
import { createDatabase } from './db.js';
import { createApp } from './app.js';

const { STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, ADMIN_TOKEN, APP_URL = 'http://localhost:3000',
  DATABASE_PATH = './cookist.sqlite', PORT = '3000' } = process.env;

if (!STRIPE_SECRET_KEY?.startsWith('sk_test_') || !STRIPE_WEBHOOK_SECRET?.startsWith('whsec_') ||
    !ADMIN_TOKEN || ADMIN_TOKEN.length < 24 || ADMIN_TOKEN === 'replace-with-a-long-random-secret') {
  throw new Error('Set a Stripe test secret key, webhook secret, and a unique admin token of at least 24 characters in .env.');
}
const url = new URL(APP_URL);
if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
  throw new Error('APP_URL must be an HTTP(S) origin without a path, credentials, query, or fragment.');
}
const app = createApp({
  db: createDatabase(DATABASE_PATH),
  stripe: new Stripe(STRIPE_SECRET_KEY),
  adminToken: ADMIN_TOKEN,
  appUrl: url.origin,
  webhookSecret: STRIPE_WEBHOOK_SECRET
});
app.listen(Number(PORT), '127.0.0.1', () => console.log(`Cookist is running at http://localhost:${PORT}`));
