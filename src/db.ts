import { DatabaseSync } from 'node:sqlite';

export function createDatabase(path: string) {
  const db = new DatabaseSync(path);
  db.exec(`
    PRAGMA foreign_keys = ON;
    CREATE TABLE IF NOT EXISTS sellers (
      id INTEGER PRIMARY KEY,
      name TEXT NOT NULL,
      kitchen TEXT NOT NULL,
      permit_reference TEXT NOT NULL,
      stripe_account TEXT NOT NULL UNIQUE,
      approved INTEGER NOT NULL DEFAULT 0
    );
    CREATE TABLE IF NOT EXISTS meals (
      id INTEGER PRIMARY KEY,
      seller_id INTEGER NOT NULL REFERENCES sellers(id),
      name TEXT NOT NULL,
      description TEXT NOT NULL,
      allergens TEXT NOT NULL,
      pickup TEXT NOT NULL,
      price_cents INTEGER NOT NULL CHECK (price_cents >= 50),
      active INTEGER NOT NULL DEFAULT 1
    );
    CREATE TABLE IF NOT EXISTS orders (
      id TEXT PRIMARY KEY,
      meal_id INTEGER NOT NULL REFERENCES meals(id),
      seller_id INTEGER NOT NULL REFERENCES sellers(id),
      meal_name TEXT NOT NULL,
      price_cents INTEGER NOT NULL,
      fee_cents INTEGER NOT NULL,
      quantity INTEGER NOT NULL,
      pickup TEXT NOT NULL,
      checkout_session TEXT UNIQUE,
      customer_email TEXT,
      status TEXT NOT NULL DEFAULT 'pending',
      created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
    );
  `);
  return db;
}
