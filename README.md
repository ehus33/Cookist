# Cookist

A Seattle shared-kitchen marketplace pilot. This is **test-mode software, not a live food business**: it only accepts Stripe test keys and is not approved for real orders. The application does not itself verify seller permits, kitchen approval, food safety, or legal compliance.

## What works

- An administrator records sellers and permit references, creates Stripe Connect Express onboarding links, manually approves or suspends sellers, and adds meals with ingredients, allergens, price, and pickup details.
- Customers can browse approved sellers' meals and use Stripe Checkout in test mode. The server calculates the price and a 10% platform fee and uses a Connect destination charge.
- Signed Stripe webhooks mark paid orders for the administrator. Stripe's processing fees are deducted from the platform's proceeds, not the seller's 90% transfer. The app does not calculate sales tax.

## Run locally

Requires Node.js 22.13 or later, a Stripe account with Connect enabled, and [Stripe CLI](https://docs.stripe.com/stripe-cli). Node's built-in SQLite module is experimental on Node 22.

1. Run `yarn install`.
2. Copy `.env.example` to `.env`. Set a unique random `ADMIN_TOKEN` of at least 24 characters and your Stripe **test** secret key. Never commit or share credentials.
3. Run `stripe listen --forward-to localhost:3000/api/webhook` and copy its `whsec_...` signing secret into `.env`. Use a new secret when the listener changes.
4. Run `yarn start`, open `http://localhost:3000/admin.html`, and unlock with your admin token.
5. Add a test seller, generate a Stripe onboarding link, and complete Connect test onboarding. Verify the seller's details independently, then approve them and add a meal. An approved seller appears in the menu but checkout is blocked until Stripe reports charges and payouts enabled.
6. Visit `http://localhost:3000` and use a [Stripe test card](https://docs.stripe.com/testing). The Stripe CLI must keep running so paid test orders appear in admin.

Run `yarn test` for automated tests or `yarn build` for a TypeScript check. The SQLite database is stored locally in `cookist.sqlite` by default; `.env` and database files are ignored by Git.

## Launch blockers

This is a local, single-operator prototype. Before real customers, get written guidance from Public Health — Seattle & King County for the exact preparation, order, and pickup flow; confirm seller and platform permits, food-safety and allergen obligations, taxes, and insurance with qualified advisers. Production needs real authentication and authorization, seller access and notifications, inventory and pickup scheduling, order status and cancellation/refunds, privacy controls, rate limiting, secure hosting, persistent backups, and monitoring. Reconcile payment failures and refunds using Stripe. The single shared admin token and local SQLite storage are not production-ready.

The app intentionally refuses live Stripe keys. A live launch requires a separate security, regulatory, and operational review; changing the key guard alone is not sufficient. Stripe Connect onboarding links are single-use and sensitive; share them privately and never post them publicly.
