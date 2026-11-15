import express from 'express';
import type { Request, Response, NextFunction } from 'express';
import type { DatabaseSync } from 'node:sqlite';
import type Stripe from 'stripe';
import { randomUUID, timingSafeEqual } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const publicDir = fileURLToPath(new URL('../public/', import.meta.url));
const assetsDir = fileURLToPath(new URL('./public/', import.meta.url));

function requiredText(value: unknown, max = 500): value is string {
  return typeof value === 'string' && value.trim().length > 0 && value.trim().length <= max;
}

function adminOnly(token: string) {
  const expected = Buffer.from(token);
  return (req: Request, res: Response, next: NextFunction) => {
    const supplied = Buffer.from(req.get('x-admin-token') || '');
    if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected)) {
      return res.status(401).json({ error: 'Invalid administrator token.' });
    }
    next();
  };
}

interface AppOptions {
  db: DatabaseSync;
  stripe: Stripe;
  adminToken: string;
  appUrl: string;
  webhookSecret: string;
}

interface Meal {
  id: number;
  seller_id: number;
  name: string;
  pickup: string;
  price_cents: number;
  stripe_account: string;
}

interface Seller {
  stripe_account: string;
}

export function createApp({ db, stripe, adminToken, appUrl, webhookSecret }: AppOptions) {
  const app = express();
  const admin = adminOnly(adminToken);
  app.disable('x-powered-by');

  app.post('/api/webhook', express.raw({ type: 'application/json' }), (req, res) => {
    let event;
    try {
      event = stripe.webhooks.constructEvent(req.body, req.get('stripe-signature') || '', webhookSecret);
    } catch (error) {
      console.error('Webhook signature verification failed:', error);
      return res.status(400).send('Invalid webhook signature');
    }
    if (event.type === 'checkout.session.completed' || event.type === 'checkout.session.async_payment_succeeded') {
      const session = event.data.object;
      if (session.payment_status === 'paid' && session.metadata?.orderId) {
        db.prepare(`UPDATE orders SET status = 'paid', customer_email = ?, checkout_session = ?
          WHERE id = ? AND (checkout_session IS NULL OR checkout_session = ?) AND status = 'pending'`)
          .run(session.customer_details?.email || null, session.id, session.metadata.orderId, session.id);
      }
    }
    res.json({ received: true });
  });

  app.use(express.json({ limit: '16kb' }));
  app.use('/assets', express.static(assetsDir));
  app.use(express.static(publicDir));

  app.get('/api/meals', (req, res) => {
    const meals = db.prepare(`SELECT m.id, m.name, m.description, m.allergens, m.pickup,
      m.price_cents, s.name AS seller, s.kitchen
      FROM meals m JOIN sellers s ON s.id = m.seller_id
      WHERE m.active = 1 AND s.approved = 1 ORDER BY m.id DESC`).all();
    res.json(meals);
  });

  app.post('/api/checkout', async (req, res, next) => {
    const { mealId, quantity } = req.body;
    if (!Number.isSafeInteger(mealId) || !Number.isSafeInteger(quantity) || quantity < 1 || quantity > 10) {
      return res.status(400).json({ error: 'Choose a meal and a quantity from 1 to 10.' });
    }
    try {
      const meal = db.prepare(`SELECT m.*, s.stripe_account FROM meals m
        JOIN sellers s ON s.id = m.seller_id WHERE m.id = ? AND m.active = 1 AND s.approved = 1`).get(mealId) as Meal | undefined;
      if (!meal) return res.status(404).json({ error: 'This meal is unavailable.' });
      const account = await stripe.accounts.retrieve(meal.stripe_account);
      if (!account.charges_enabled || !account.payouts_enabled) {
        return res.status(409).json({ error: 'This seller has not completed payment onboarding.' });
      }
      const total = meal.price_cents * quantity;
      const fee = Math.round(total * 0.1);
      const orderId = randomUUID();
      db.prepare(`INSERT INTO orders (id, meal_id, seller_id, meal_name, price_cents, fee_cents, quantity, pickup)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`).run(orderId, meal.id, meal.seller_id, meal.name, meal.price_cents, fee, quantity, meal.pickup);
      const session = await stripe.checkout.sessions.create({
        mode: 'payment',
        payment_method_types: ['card'],
        line_items: [{
          price_data: { currency: 'usd', product_data: { name: meal.name }, unit_amount: meal.price_cents },
          quantity
        }],
        payment_intent_data: {
          application_fee_amount: fee,
          transfer_data: { destination: meal.stripe_account }
        },
        metadata: { orderId },
        success_url: `${appUrl}/?checkout=success`,
        cancel_url: `${appUrl}/?checkout=cancelled`
      }, { idempotencyKey: orderId });
      db.prepare('UPDATE orders SET checkout_session = ? WHERE id = ?').run(session.id, orderId);
      res.json({ url: session.url });
    } catch (error) {
      next(error);
    }
  });

  app.get('/api/admin/sellers', admin, (req, res) => {
    res.json(db.prepare('SELECT * FROM sellers ORDER BY id DESC').all());
  });

  app.post('/api/admin/sellers', admin, async (req, res, next) => {
    const { name, kitchen, permitReference } = req.body;
    if (![name, kitchen, permitReference].every(value => requiredText(value, 200))) {
      return res.status(400).json({ error: 'Name, approved kitchen, and permit reference are required.' });
    }
    try {
      const account = await stripe.accounts.create({ type: 'express', country: 'US',
        capabilities: { card_payments: { requested: true }, transfers: { requested: true } } });
      const result = db.prepare('INSERT INTO sellers (name, kitchen, permit_reference, stripe_account) VALUES (?, ?, ?, ?)')
        .run(name.trim(), kitchen.trim(), permitReference.trim(), account.id);
      res.status(201).json({ id: Number(result.lastInsertRowid) });
    } catch (error) { next(error); }
  });

  app.post('/api/admin/sellers/:id/onboarding', admin, async (req, res, next) => {
    const seller = db.prepare('SELECT * FROM sellers WHERE id = ?').get(req.params.id) as Seller | undefined;
    if (!seller) return res.status(404).json({ error: 'Seller not found.' });
    try {
      const link = await stripe.accountLinks.create({
        account: seller.stripe_account,
        refresh_url: `${appUrl}/admin.html`,
        return_url: `${appUrl}/admin.html`,
        type: 'account_onboarding'
      });
      res.json({ url: link.url });
    } catch (error) { next(error); }
  });

  app.patch('/api/admin/sellers/:id', admin, (req, res) => {
    if (typeof req.body.approved !== 'boolean') return res.status(400).json({ error: 'Approval must be true or false.' });
    const result = db.prepare('UPDATE sellers SET approved = ? WHERE id = ?').run(Number(req.body.approved), req.params.id);
    if (!result.changes) return res.status(404).json({ error: 'Seller not found.' });
    res.json({ ok: true });
  });

  app.post('/api/admin/meals', admin, (req, res) => {
    const { sellerId, name, description, allergens, pickup, priceCents } = req.body;
    if (!Number.isSafeInteger(sellerId) || ![name, description, allergens, pickup].every(value => requiredText(value)) ||
        !Number.isSafeInteger(priceCents) || priceCents < 50 || priceCents > 1000000) {
      return res.status(400).json({ error: 'Complete all meal fields and provide a valid price in cents.' });
    }
    if (!db.prepare('SELECT id FROM sellers WHERE id = ?').get(sellerId)) {
      return res.status(404).json({ error: 'Seller not found.' });
    }
    const result = db.prepare(`INSERT INTO meals (seller_id, name, description, allergens, pickup, price_cents)
      VALUES (?, ?, ?, ?, ?, ?)`).run(sellerId, name.trim(), description.trim(), allergens.trim(), pickup.trim(), priceCents);
    res.status(201).json({ id: Number(result.lastInsertRowid) });
  });

  app.get('/api/admin/orders', admin, (req, res) => {
    res.json(db.prepare(`SELECT o.*, s.name AS seller FROM orders o JOIN sellers s ON s.id = o.seller_id
      WHERE o.status = 'paid' ORDER BY o.created_at DESC LIMIT 100`).all());
  });

  app.use((error: unknown, req: Request, res: Response, next: NextFunction) => {
    console.error(error);
    if (res.headersSent) return next(error);
    res.status(500).json({ error: 'The request could not be completed. Check the server logs.' });
  });
  return app;
}
