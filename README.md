# Cookist

Cookist is being refocused on deployments. The repository currently contains a
neutral web and native workspace, a Go gateway, and a Kotlin authentication
service. An optional owner-default worker can create Terraform-managed sites,
with configurable tenant roles and entitlement checks.
It is disabled until explicitly configured.

The AWS demo uses the neutral workspace and experimental Cognito authentication.
Keep it private; it is not configured for production use.

The private hosted test now runs the deployment gateway and core together on
the existing EC2 worker, behind the Lightsail/Tailscale reverse proxy.
The gateway uses short-lived EC2 instance-role credentials with the scoped
test-site policy, not administrator SSO credentials or stored access keys.
The currently hosted build uses operator-only access and the four-site cap
below. Encrypted EBS and daily encrypted snapshots protect the state volume.
The hosted gateway's `ALLOWED_ORIGINS` must include `https://trymachi.com`;
otherwise browser deployment submissions are rejected even when listing works.
The website remains Tailscale-private and can be used from a phone connected
to the same tailnet. Native apps remain placeholder shells.

This is still a private test configuration, not a production launch.
Persistent CloudTrail/data-event logging and CloudWatch monitoring are not
configured; complete the logging and monitoring requirements below before
real-user or production use. Site provisioning must be tested separately:
configuration checks do not create a site or prove a full Terraform apply.

## Source layout

- [App](apps/web/src/app/): application screen and its integration tests.
- [Authentication](apps/web/src/features/auth/): Cognito sign-in and session handling.
- [Builder](apps/web/src/features/builder/): visual editor, page blocks and startup sections.
- [Shared](apps/web/src/shared/): API helpers, domain validation and shared types.
- [Gateway assets](services/gateway/assets/): styles shared by previews and generated sites.

Feature tests live alongside their source. Gateway Go files stay in one directory
because they share a package; Terraform modules remain in its `terraform/` folder.
Root manifests, environment configuration and runtime data stay in their existing locations.

## Run on Windows

Requires Node.js 22.12+ (or 24+), Yarn 1.22.22, JDK 17, and Go 1.24+. Run:

```powershell
.\scripts\start.ps1
```

Open http://localhost:8080. For a frontend development server, run
`yarn dev` in another terminal. Vite proxies API requests to the gateway.
Copy [.env.example](.env.example) to `.env` for optional local configuration;
do not commit populated secrets.

For containers, run `docker compose up --build` and open
http://localhost:8080. The gateway binds to loopback.

## Continuous integration

[CI](.github/workflows/ci.yml) runs on pushes, pull requests and manual dispatch.
It tests and builds the frontend with Node 24 and Yarn 1.22.22, tests/vets/builds
the Go gateway, and then builds the gateway container to check its packaging.
Actions are pinned to release commits and have read-only repository permissions.

This workflow does **not** deploy, publish images, use AWS credentials or enable
live billing. The container build uses the Dockerfile's demo-auth default and is
only a packaging check, not a production deployment artifact. Restoring the
newer dynamic-app and actual-cost billing source remains a separate deployment
prerequisite.

## Managed Terraform deployments

When enabled, **New deployment** starts a background Terraform apply for a
blank site, a simple business listing, a product storefront, or a visual-builder
page. The worker creates a **public**
CloudFront site backed by a private S3 bucket. Anyone with the URL can access
the site; it is not Tailscale-private. Blank and business templates do not provide
payments. Storefronts open seller-owned Stripe Payment Links; Cookist does not
process sales, store card details, or provide a cart, inventory database, or an
editor for existing deployed sites. AWS charges continue
until the resources are destroyed; see [S3 pricing](https://aws.amazon.com/s3/pricing/)
and [CloudFront pricing](https://aws.amazon.com/cloudfront/pricing/).

Choose a content template and then a visual style: **Classic** (the existing
simple look), **Modern** (blue accents and rounded cards), **Warm** (cream tones
and serif typography), or **Bold** (dark surfaces and purple accents). The
creation form previews each style and updates as you enter business details.
Preview placeholders are not deployed; blank templates remain empty.
Generated sites include an embedded favicon, so browsers do not request a
missing `favicon.ico` from the private S3 origin.

### Selling products

Choose **Storefront**, enter a store name, and add up to 20 products with names,
optional descriptions, USD prices, and Stripe Payment Links. The themed preview
shows the catalog but disables checkout; deployed **Buy now** buttons open Stripe.
In **your own Stripe account**, create a one-time Payment Link for each offering
at [Stripe Payment Links](https://dashboard.stripe.com/payment_links). Paste the
plain `https://buy.stripe.com/...` URL without query parameters. Match the
displayed price to its Stripe price; the hosted checkout is authoritative.
Use `https://buy.stripe.com/test_...` links for testing; generated pages label them
as test checkouts. Real sales require live links and an activated seller account.

Store payments go to the account that owns each link, independently of Cookist's
subscription billing and sandbox setting. No seller secret keys are requested.
Configure shipping/address collection, taxes, receipts, and payment limits in
Stripe. Sellers manage fulfillment and refunds there; Cookist does not verify
stock or mark orders paid. Existing blank/business deployments are not converted.

### Designing a page visually

#### Startup tools

- **Save draft / Load draft / Delete saved draft** keep one private design per
  authenticated account and organization on the worker's persistent volume.
  Incomplete content and product prices can be saved. Saving is explicit, not
  autosave, and never changes the published site. A draft of an existing site
  retains its edit target; it cannot silently become a new deployment.
- **Startup sections** insert editable Hero, Features, Pricing, Testimonials,
  and FAQ layouts using standard elements. One Undo removes the whole inserted
  section. Replace sample copy and supply button destinations before publishing;
  sections never silently link visitors to an example website.
- **Edit [site name] / Publish changes** reopen builder sites whose structured
  design was saved by this release. Region, URL, AWS resources, and ownership are
  retained. The worker applies only a reviewed saved Terraform plan that updates
  the existing `aws_s3_object.index` in place; infrastructure drift or replacement
  is rejected. Planning failures keep the published design and report an error.
  Uncertain apply/persistence failures retain a recovery lock for an operator.
  Older sites without a saved structured design cannot be reopened automatically.
- **Contact form / Waitlist form** collect names, emails, contact messages, and
  explicit contact consent. Published forms submit natively to the gateway;
  editing and preview never send submissions. Owners and authorized deployment
  managers use **Submissions for [site name]** to read or permanently delete
  entries in the private organization inbox. Forms do not send email, subscribe
  users to a mailing service, or provide a CRM automatically.

Lead forms require `COOKIST_PUBLIC_ORIGIN` to be the reachable HTTPS portal origin
(for example, `https://trymachi.com`, with no trailing slash or path). The reverse
proxy must forward `POST /forms/{deploymentId}/{formId}` to the gateway. This route
is public only for a published form on a ready site, requires that site's exact
browser Origin, accepts at most 8 KiB, and validates bounded fields and consent.
A honeypot rejects obvious bot submissions; each form accepts at most 30 valid
submissions per minute and each site stores at most 1,000 entries until managers
delete them. Rate counters are in memory; the inbox capacity persists across
restarts. These are basic spam controls, not a CAPTCHA or a substitute for an
edge rate limiter. Display an appropriate privacy policy and delete records when
no longer needed. Site cleanup also removes its saved design and submissions;
the separate account draft is retained.

Drafts use `GET`, `PUT`, and `DELETE /api/builder-draft`. Designs use
`GET` and `PATCH /api/deployments/{id}/design`. Inbox access uses
`GET /api/deployments/{id}/submissions` and
`DELETE /api/deployments/{id}/submissions/{submissionId}`. All these API routes
require authentication and enforce account/organization permissions.

Choose **Visual builder** to open Cookist's Page studio. The left-hand Add elements
sidebar has square icon tiles with visible labels; it stays within reach while
scrolling on desktop and becomes a compact tile grid above the page on phones.
The desktop editor uses the available screen width and a taller center canvas,
with reduced outer padding. Phone preview remains capped at 390px; published
site dimensions and non-builder screens are unchanged.
Drag a **Heading**,
**Text**, **Image**, **Button**, **Divider**, **Space**, or **Product catalog**
from Add elements onto a marked drop zone. Drag an element's **Drag** handle
to reorder it. Drop to the **Left** or **Right** of a single element to create
an equal-width two-element row; any mix of element types can share a row.
Full rows accept no third column. You can also select a single element and
choose **Add placement → Left/Right of selected** before clicking Add, or use
**Place beside previous** for an existing element. **Stack row vertically**
splits a pair back into separate rows. You can also click Add and use **Up** /
**Down**, including on touch screens and with a keyboard. Select **Edit** or click an element to
change its content, alignment, and size in Element settings. Duplicate or delete
elements, undo/redo changes, and use **Phone preview** or **Preview page** before
deploying. Links and checkout buttons do not navigate while editing or previewing.

Up to 40 elements are supported, including one product catalog using the same
seller-owned Stripe checkout configuration as Storefront. Images require an HTTPS
URL and alternative text; use media you own or have permission to publish.
Images load from the supplied host (not uploaded to Cookist), so that host must
allow public image access and will receive browser requests. Buttons require
HTTPS destinations. Arbitrary HTML, scripts, embeds, and custom CSS are not
accepted. The gateway validates every element and escapes its content.

This is a responsive block editor with at most two elements per row: it
intentionally avoids overlapping absolute positioning. Pairs stack in reading
order on screens up to 600px wide and in Phone preview. **Deploy site** publishes
the current layout to a new S3/CloudFront site. Drafts remain only in the open
form, not across page reloads. Existing deployments are not edited or replaced.
Cookist's original implementation uses general interaction patterns described
in [Wix's public element-editing documentation](https://support.wix.com/en/article/wix-editor-working-with-elements).
That documentation was observed through REA; no signed-in Wix editor, proprietary
source, or Wix assets were inspected or copied.

The selected style is saved with the deployment and embedded in its generated
HTML, with no external fonts or styling service required. Styles apply to new
sites only; existing sites are unchanged. API clients may send `style` as
`classic`, `modern`, `warm`, or `bold`; omitting it preserves Classic.
Unsupported styles are rejected before provisioning.

During the test phase, the worker permits **at most four site deployments total
across all users**, not four per user or four individual AWS resources. Each
site creates multiple AWS resources. The server checks the cap while holding
the shared worker lock and rejects additional creation requests with HTTP 429
(`deployment_limit`) before starting Terraform. Existing, running, failed,
and pending deployment directories all reserve slots, even if metadata is
missing or invalid. The cap survives restarts; it does not delete existing sites
or automatically free a slot after manual destruction. Successful portal cleanup
removes the deployment directory and frees its slot.

Use one durable `COOKIST_DEPLOYMENT_ROOT` for all workers and users; separate
directories, installations, or Terraform runs outside the worker are not covered
by this cap. Never manually delete state to bypass the limit. This is a conservative test-phase active-site
cap, not an AWS account-wide quota, spending limit, or automatic cleanup policy.
Deployment access defaults to the configured operator. Optional server-managed
RBAC/ABAC grants tenant-scoped customer access as described below.

### Deployment RBAC and ABAC

`COOKIST_DEPLOYMENT_OWNER` identifies the platform owner by the full verified
Cognito actor ID returned by `/api/me`, not an email or a browser-provided role.
The owner can view/manage all deployments and bypass subscription and tenant
quotas, but not the shared four-site cap or worker serialization.

Leave `COOKIST_DEPLOYMENT_ACCESS_FILE` empty to keep everyone else denied.
To grant customer access, set it to an absolute path to a private JSON policy:

```json
{
  "version": 1,
  "members": {
    "<verified-actor-id>": { "tenantId": "example-org", "role": "deployer" }
  },
  "tenants": {
    "example-org": {
      "subscriptionStatus": "inactive",
      "deploymentLimit": 1
    }
  }
}
```

Replace the placeholder with the verified actor ID. Roles are `customer_admin`,
`deployer`, and `viewer`. All members can list only their tenant's deployments.
Customer admins and deployers can create with an active entitlement and delete
completed deployments belonging to that tenant; viewers cannot mutate resources.
Customer-admin membership does not grant platform administration. When Stripe is
configured, only customer admins can open their tenant's checkout and billing
portal. Membership changes remain operator-managed.
Each identity belongs to one tenant; `owner` is reserved for legacy owner resources.

Without Stripe enabled, to grant an active entitlement, use `"subscriptionStatus": "active"` and a
`"validUntil"` RFC3339 UTC timestamp. Creation is denied at expiry. Inactive
entitlements must omit `validUntil`. Viewing and authorized deletion remain
available after expiry to allow cost cleanup. Unknown identities are denied.
The web app shows access-required, expired-entitlement, or read-only notices
and hides/disables unavailable actions; backend checks remain authoritative.
`GET /api/deployment-access` returns the caller's permissions, never the member directory.

The policy is re-read and validated on each request; invalid/missing configured
files fail closed (HTTP 503), including for the owner. Replace it atomically so
requests never observe a partially written policy. Restrict write access to the
operator and mount it read-only into containers at the configured path using a
deployment-specific Compose override. Do not store this file in the writable
Terraform state volume or commit populated identities/entitlements.

New deployment metadata contains server-assigned `tenantId` and `createdBy`.
Legacy metadata without both fields is treated as owned by the platform owner;
it is never assigned to a customer. Original Terraform configuration/state is
not rewritten. Cross-tenant deletion returns HTTP 404. Tenant quotas include
running/failed/retained deployments and are checked under the same exclusive
worker lock as the global cap. Unverifiable ownership blocks customer creation.

The access-policy file manages membership and quotas, **not payment collection**.
When Stripe is enabled, Stripe-derived entitlement records override manual
subscription grants for hosted customers. Neither checkout redirects nor
client-supplied subscription flags, roles, or tenant IDs grant access. The shared worker AWS
identity is unchanged: this is application-level isolation, not separate AWS
accounts, per-tenant IAM identities, or a production multi-tenant security boundary.

### Hosted paywall and local development

`COOKIST_DEPLOYMENT_ACCESS_MODE` defaults to `hosted`, even when the gateway
binds locally. Hosted mode requires active entitlements for customer deployment
features; the platform owner remains exempt. Unpaid customers see a paid-access
notice instead of creation controls/site links. The backend omits site URLs for
unpaid members. Recovery records and role-authorized cleanup remain accessible
after expiry so the paywall cannot trap billable resources.

For trusted native local development only, explicitly set:

```dotenv
HOST=127.0.0.1
COOKIST_DEPLOYMENT_ACCESS_MODE=local
```

Local mode skips only the subscription condition for configured memberships.
Authentication, roles, tenant ownership, tenant quotas and the global cap still
apply. Unknown users remain denied. Startup rejects local mode unless `HOST`
is an explicit loopback IP (`127.0.0.1` or `::1`); it never decides from request
Host/Origin/forwarded headers or the browser's hostname. Do not expose a local
gateway through tunnels, reverse proxies or port forwarding. Compose explicitly
uses hosted mode because the container listens on `0.0.0.0`, even when its
published host port is loopback-only.

This gates the Cookist application, not the generated public S3/CloudFront
sites. Previously shared site URLs remain public until resources are deleted.
This setting alone does not configure payment collection; use the separate
Stripe integration below.

### Stripe subscription paywall

Cookist uses Stripe's official Go SDK, hosted subscription Checkout and Customer
Portal. Card details never enter Cookist. Billing is disabled by default and
requires an enabled, Cognito-authenticated deployment worker. Nothing is charged
by installing this code.

1. Start in Stripe **test mode**. Create an active, fixed recurring, licensed
   subscription price. Set `COOKIST_STRIPE_PRICE_ID` to its `price_...` identifier.
   Stripe displays the actual configured price/currency in Checkout; Cookist does
   not invent a price or accept one from the browser.
2. Set private gateway runtime values `COOKIST_BILLING_ENABLED=true`,
   `COOKIST_STRIPE_MODE=test`, `COOKIST_STRIPE_SECRET_KEY=sk_test_...`,
   `COOKIST_STRIPE_WEBHOOK_SECRET=whsec_...`, and
   `COOKIST_PUBLIC_ORIGIN` to the exact HTTPS application origin (no path or
   trailing slash). Test mode also permits explicit loopback HTTP IP origins.
   Configure `ALLOWED_ORIGINS` separately for that origin. Never put secret keys
   in `VITE_` variables, browser code, build arguments or committed files.
3. Configure Stripe Customer Portal for subscription/payment-method management.
   Keep quantity fixed at one and price changes disabled unless the operator
   updates Cookist's configured price. A canceled subscription can be purchased
   again; an existing active, trialing, paused, past-due, unpaid or incomplete
   subscription blocks duplicate checkout. Use the portal to resolve billing;
   an operator may need to resolve/cancel an incomplete payment in Stripe first.
4. Register a **snapshot** webhook endpoint at
   `https://<webhook-host>/api/billing/webhook`, using API version
   `2026-11-18.endive` (the installed Stripe SDK's API version). Subscribe to
   `checkout.session.completed`, `checkout.session.async_payment_succeeded`,
   `checkout.session.async_payment_failed`, `customer.subscription.created`,
   `customer.subscription.updated`, `customer.subscription.deleted`,
   `invoice.paid`, and `invoice.payment_failed`. Copy that endpoint's signing
   secret into private runtime configuration. Raw-body signatures, timestamp,
   mode, API version and customer mapping are verified; connected-account events
   are not supported. Do not disable these checks.
5. Choose customer onboarding. The default `COOKIST_SELF_SERVICE_BILLING=false`
   requires provisioning verified Cognito actors as `customer_admin`, `deployer`
   or `viewer` in the access policy, including their tenant quota. To allow
   self-service signup, explicitly set `COOKIST_SELF_SERVICE_BILLING=true` with
   configured hosted Stripe billing. Verified actors without assigned membership
   receive an isolated, server-derived personal tenant with a one-deployment
   limit and billing-admin rights only for that tenant. They are not platform
   administrators, and they cannot deploy without payment. Existing policy
   memberships take precedence, including viewer restrictions. Tenant IDs are
   stable across restarts; conflicts with policy-managed tenant IDs fail closed.
   Self-service is not available in local bypass mode. Customer admins select
   **Subscribe with Stripe**, then follow the secure Checkout link. Only a paid
   invoice on an active subscription with the configured price and quantity one
   unlocks that tenant until its paid period ends. No trial/grace access is
   granted. Returning from Checkout is not evidence of payment: the existing
   permission polling displays access only after a verified webhook refreshes
   authoritative Stripe state. Owner and explicit local access remain exempt.

**Private hosting caveat:** the existing Tailscale-private site cannot receive
Stripe's public webhook delivery. Live billing is not ready until a separately
authorized, public HTTPS ingress forwards **only POST `/api/billing/webhook`**
to the gateway, preserving the raw body and `Stripe-Signature`, with a 1 MB body
limit. Do not expose the whole private app or use a local-bypass gateway. For
development, use Stripe CLI forwarding to a private/loopback test gateway and
the CLI's own signing secret. No public ingress is created by this integration.

Customer/checkout mappings and entitlement records are stored privately under
`<deployment-state>/.billing/<test|live>` in the durable deployment volume.
Back up this directory along with deployment state; do not delete it when
cleaning up sites. Test/live mappings are separate. Requests are serialized by
per-tenant lock files; Checkout uses persisted Stripe idempotency keys and reuses
open sessions. Duplicate/out-of-order webhooks re-fetch current subscriptions
instead of applying stale event snapshots. Stripe/storage errors fail explicitly
and webhook errors are retried, rather than granting access.

If the worker crashes holding a billing lock, stop all gateway instances, verify
there is no active billing request, then have the operator remove only the
affected tenant's `.lock` file before restarting. Do not remove customer records.
If changing the configured price while an older Checkout is open, first expire
that session in Stripe; Cookist rejects the old-price session rather than
charging an unintended price. Price changes do not migrate subscriptions.
Missed webhooks may leave the last confirmed grant valid until its paid expiry;
monitor webhook delivery and retry failed events. Expired/unpaid members retain
role-authorized deployment listing and cleanup so the paywall does not trap AWS
resources. Stripe payments do not cover or cap underlying AWS usage charges.

After verifying Checkout, renewals, cancellation and portal behavior in test
mode, configure separate live keys, price, webhook endpoint/signing secret and
`COOKIST_STRIPE_MODE=live`. This repository does not supply those credentials,
create a Stripe account, configure public ingress or deploy the integration.

#### Current hosted webhook ingress

The existing Lightsail proxy now has a manually configured, public HTTPS
webhook-only entrance:

```text
https://ip-172-26-9-217.tail18ecfa.ts.net:8443/api/billing/webhook
```

Tailscale Funnel on port 8443 forwards to an Nginx listener bound only to
`127.0.0.1:18084`. That listener forwards only the exact, query-free
`POST /api/billing/webhook` path to the private worker. Other paths return 404;
other methods on the webhook path return 405. Nginx preserves the request body
and Stripe signature header, rejects bodies larger than 1 MiB, and rate-limits
the ingress to 10 requests/second with a burst of 20. No AWS firewall ports or
new AWS resources were added. The existing application stays private on port
443; port 8443 avoids replacing its HTTPS listener.

Funnel permission is restricted to `tag:cookist-webhook`, assigned only to the
Lightsail proxy. The owner's existing SSH access and other network grants were
preserved. Funnel is beta and subject to bandwidth limits; monitor Stripe's
delivery retries. Nginx and Tailscale are enabled services, and the Funnel
configuration is persistent.

The existing worker now runs `cookist-gateway:startup-4407cc45239e`, with image ID
`sha256:1ef6a7433920aa4f8e7aef0ec6aeeb83fade8d9672c66c1f89e9d280b3e73c1f`.
It uses the archived source at commit
`56d015521ca396b0614ff24596befb2599e50f9f` plus the current web 1.7.2 package
metadata and validated generated-site favicon, storefront, visual-builder,
two-column elements, square-icon sidebar, expanded canvas, and startup tools.
Hosted customers can
choose Classic, Modern, Warm, or Bold styles with live previews, including on
mobile. Billing uses the Glaucous Zipline
**test sandbox**, not live payments. Its enabled snapshot destination listens
to the eight events above, and its default sandbox Customer Portal is configured.
Unsigned webhook POSTs return 400; accepted signed billing events return 204.
Sandbox Checkout, webhook-confirmed access, portal access and cancellation
revocation have been exercised against the hosted application. Style selection,
preview colors, mobile layout, API style validation and selected-style submission
were verified without creating any AWS sites.
The deployed visual editor was verified using isolated API fixtures: actual
palette and reorder drags, editable settings, undo/redo, duplicate/delete,
all original seven element types, left/right placement, moving existing elements beside
others, the two-column limit, phone placement controls, phone layout, preview,
and exact deployment payload. Generated builder HTML was separately verified
locally in all four styles, including equal-width desktop columns, stacking at
600px and below, and no horizontal overflow at 390px.
These editor checks did not create AWS sites or make payments.
The deployed icon sidebar was verified at 1440, 900, 641, 640, 390, and 320px:
square labeled tiles, desktop/tablet sidebar placement, phone grid, visible
keyboard focus, sticky scrolling, actual tile dragging, and no horizontal
overflow. The existing builder and paired-element browser checks also passed.
The startup release passed 41 frontend tests, the full Go test suite and vet,
and a production frontend build. Hosted browser checks used isolated API fixtures
to verify section insertion, drafts across reload, fresh editor history, existing
site update payloads, private inbox controls, contact/waitlist previews, and mobile
layout; the original builder, pairs, and sidebar regressions also passed.
Actual generated contact and waitlist forms were separately submitted cross-origin
to a local TLS gateway, with private persistence and authenticated inbox retrieval.
Hosted `/forms/` POST ingress returned the gateway's expected unknown-form 404;
private draft/inbox requests without authentication returned 401. These checks
did not provision sites or create production submissions. Forms collect entries
in the private inbox, not an email service or CRM; older sites without a saved
structured design cannot automatically reopen in the editor.
Storefront product editing and submission were exercised against the deployed
frontend with isolated API fixtures. Actual Go-generated storefront pages were
verified in all four styles at 390px width, and a real one-time Stripe sandbox
checkout completed for $19.99 USD. Temporary payment links were disabled and
test purchases refunded; this validation did not create a storefront on AWS.

Test credentials are stored as SSM SecureString parameters under
`/cookist/billing/test/`; the existing worker role can read only the two required
parameters. Private runtime configuration and a read-only access-policy mount
keep credentials and tenant membership out of the image and browser. The
existing deployment-state volume, private networking and owner exemption remain
unchanged. `COOKIST_SELF_SERVICE_BILLING=true` is enabled on this worker. Fresh
verified accounts can subscribe in their own isolated tenants without a manual
policy entry; this does not grant platform administration or unpaid deployments.
The flow was verified with a fresh account while the policy had no memberships.
Other installations remain administrator-approved unless they explicitly enable
the setting described above.

The previous expanded-canvas gateway is retained as
`cookist-gateway-before-startup-4407cc45239e`, with environment and startup backups
ending in `.before-startup-4407cc45239e`. The previous sidebar gateway is retained as
`cookist-gateway-before-canvas-3d20d8b7404f`, with environment and startup backups
ending in `.before-canvas-3d20d8b7404f`. The previous paired-builder gateway is retained as
`cookist-gateway-before-sidebar-4908fe4317b2`, with environment and startup backups
ending in `.before-sidebar-4908fe4317b2`. The previous stacked-builder gateway is retained as
`cookist-gateway-before-paired-a27f19cc3554`, with environment and startup backups
ending in `.before-paired-a27f19cc3554`. The previous storefront gateway is retained as
`cookist-gateway-before-builder-4ac4bb44b042`, with environment and startup backups
ending in `.before-builder-4ac4bb44b042`. The previous favicon gateway is retained as
`cookist-gateway-before-storefront-50783e25fa4a`, with environment and startup
backups ending in `.before-storefront-50783e25fa4a`. The previous styles gateway is retained as
`cookist-gateway-before-favicon-92b6a1fb36d7`, with environment and startup backups
ending in `.before-favicon-92b6a1fb36d7`. The previous self-service gateway is retained as
`cookist-gateway-before-styles-56d0155`, with environment and startup backups ending
in `.before-styles-56d0155`. The previous billing gateway is retained as
`cookist-gateway-before-selfserve-83fbde81bf4e`, with environment and startup
backups ending in `.before-selfserve-83fbde81bf4e`. The older cleanup rollback
`cookist-gateway-before-billing-a314b10` and its backups are also retained.
Before an operator rolls
back, stop or finish all active deployment/billing work; restore both backups
and the previous container without deleting the shared deployment-state volume.
This rollout is manual: later merges do not update the running image.

To disable only this public entrance on the Lightsail proxy:

```sh
sudo tailscale funnel --https=8443 off
```

This leaves the private application and deployment state unchanged.

Select a commercial AWS Region for the S3 bucket. The list is not filtered by
account: some regions require opting in before use. CloudFront is global.
See [AWS Regions](https://docs.aws.amazon.com/general/latest/gr/rande.html)
and [enabling Regions](https://docs.aws.amazon.com/accounts/latest/reference/manage-acct-regions.html).

The reusable infrastructure lives in
[services/gateway/terraform/site](services/gateway/terraform/site). Go embeds
the `.tf` files into the gateway binary at build time, so both local and
container workers use the same templates and provider lock file without a runtime
source-tree dependency.
Rebuild the gateway after editing them. Each new deployment receives a private
copy plus `terraform.tfvars.json` containing the account, deployment ID, region,
and rendered HTML. Blank sites have an empty main section; business sites show
the business name, list title (for example Menu, Products, or Services), and
one item per non-empty line. HTML is escaped by the gateway; variable values
are literal data, not Terraform expressions. Storefronts use `businessName` and
`products` (1–20 entries with `name`, optional `description`, integer `priceCents`
from 1 to 99999999 in USD, and a plain `paymentLink` at `buy.stripe.com`). Products
are accepted for `template: "storefront"` or a visual-builder product catalog.
Visual pages use `template: "builder"` and `blocks` (1–40 entries with unique `id`,
`type`, `text`, `url`, `alt`, `align`, and `size`). Types are `heading`, `text`,
`image`, `button`, `divider`, `spacer`, and `products`; alignment is `left`,
`center`, or `right`; size is `small`, `medium`, or `large`. Image/button URLs
must use HTTPS without embedded credentials. Blocks are rejected on other
templates; product data requires a catalog block on builder pages.
An optional boolean `beside: true` places a block in the right column beside
the preceding block. The first block cannot use it, and two consecutive
`beside: true` blocks are rejected. Omitted or false values start a new row,
preserving layouts from the original builder.
All templates share the same
S3/CloudFront resources.

Existing deployment directories are not rewritten. Their original configuration
and state remain authoritative for recovery or destruction. To review a new
deployment manually, use `terraform plan -input=false -out=review.tfplan` in its
private directory with the authorized credentials, then review that plan with
[your Terraform reviewer skill](skills/terraform-plan/SKILL.md). Plans and state
can contain site content; do not commit or share them. The portal worker still
applies automatically when enabled; this extraction does not add a plan-approval
step or enable deployments.

To validate the templates without AWS credentials or resource allocation (the
mock tests require Terraform 1.7+):

```powershell
Push-Location services\gateway\terraform\site
terraform init -backend=false -input=false
terraform fmt -check
terraform validate
terraform test
Pop-Location
```

The worker needs all of the following before it can be enabled:

- Cognito mode on both services, and one trusted operator's exact `actorId`
  from `/api/me`. Self-registration does not grant deployment permission.
- A dedicated, least-privilege AWS identity with **short-lived credentials** for
  Terraform. No credentials or IAM role are created by this repository. Do not
  use the administrator SSO profile or long-lived keys for the hosted service.
  The current Lightsail host does not supply an application IAM role; a
  separately configured credential-bearing host is required. The worker runs
  inside the gateway process, so hosting it elsewhere also requires routing the
  gateway to core over a private, authenticated TLS connection.
- A private, durable, encrypted and backed-up state directory. Each deployment
  stores its configuration, state, status, and Terraform log there. On Windows,
  also restrict the directory ACL to the worker account. The process must have
  read/write access. Never share this directory with untrusted users.
- `COOKIST_DEPLOYMENTS_ENABLED=true`, `COOKIST_DEPLOYMENT_OWNER` (the trusted
  `actorId`), `COOKIST_AWS_ACCOUNT_ID` (12 digits), and absolute paths in
  `COOKIST_DEPLOYMENT_ROOT` and `COOKIST_TERRAFORM_PATH`. Terraform checks the
  target account ID before applying. Buckets are named
  `cookist-<account-id>-<deployment-id>` to support scoping permissions.
  Install Terraform separately for local
  runs; the gateway container includes Terraform and mounts `deployment-state`
  at `/app/deployments`. Configure AWS's standard credential chain on the
  worker host without committing secrets. Back up this volume securely; do not
  run `docker compose down -v`, which deletes it.

### Local scoped worker role

[terraform/worker](services/gateway/terraform/worker) provisions only the local
deployment role and its inline policy. Bootstrap it separately with an authorized
administrative SSO profile, an explicit account ID, and the exact trusted IAM
role ARN. Keep its Terraform state in a private directory outside the source
tree. Review its saved plan before applying; do not apply the site plan while
setting up the role.

The policy was narrowed from IAM Policy Autopilot's site-plan baseline:
no IAM administration, `iam:PassRole`, KMS, access-point, Object Lambda, or
S3 Express permissions are granted. S3 access is limited to this account's
`cookist-<account-id>-*` bucket namespace and `index.html` objects. New CloudFront
distributions require the reserved `CookistManaged=test-site` tag; configuration
reads, updates (needed to disable a distribution), and deletion require that tag.
The read-only `GetDistribution` check uses a wildcard resource without a tag
condition: after deletion, CloudFront cannot resolve the resource's ARN or tags,
and Terraform must receive `NoSuchDistribution` rather than `AccessDenied`.
`aws:ResourceAccount` restricts this read to the configured account when AWS
supplies resource-account context; missing-resource checks also remain allowed.
Do not assign the reserved tag to unrelated distributions.
CloudFront creation uses resource `*` where the API cannot resource-scope it.
Origin access controls cannot be tag-scoped: the worker can create, read, and
delete them within this account. Cleanup uses only resources in the original
deployment state. Reapply the worker role's updated permissions before using
cleanup; existing hosted inline policies are not changed by rebuilding the app.

Create a separate AWS profile with `role_arn` set to the new worker role and
`source_profile` set to the bootstrap SSO profile. Set `AWS_PROFILE` in local
`.env` to that **worker** profile, not the administrator profile. Its short-lived
role credentials refresh through your SSO session; renew the SSO login before
deploying if the session expires. This is only for a trusted developer's local
machine: the same OS account still owns the source SSO profile. A hosted worker
must instead use a dedicated credential-bearing identity without access to an
administrator's profile or cache.

Restrict the deployment and bootstrap state directories' Windows ACLs to the
worker account and SYSTEM, and ensure disk encryption and secure backups are
configured before storing real site data. Enable CloudTrail, S3 access/data-event
logging, and CloudWatch monitoring before real use. The scoped role and four-site
cap are not a billing cap or a production isolation boundary.

The worker runs one deployment or cleanup at a time and persists its status. A failed
apply or interrupted process may leave billable resources and partial state.
The worker retains `.worker.lock` in those cases and refuses further deployments.
Use **Delete resources** on a ready or failed deployment and confirm permanent
deletion. `DELETE /api/deployments/<id>` requires an authorized management role
and matching tenant (or the platform owner) and returns HTTP 202
with `deleting` status. Terraform runs `init` with the original provider lock
file, then `destroy` using the original configuration, variables, and state.
CloudFront disabling/deletion can take several minutes; cleanup has a 90-minute
timeout. Only after destroy succeeds and state contains no managed resources
does the worker remove the local deployment directory and release the slot.
This does not delete the Cookist servers or refund incurred charges.

A failed cleanup becomes `cleanup_failed`: state, logs, and the worker lock are
retained. Fix the logged cause (including missing permissions), then use
**Retry cleanup**. Missing/corrupt state is never treated as successful cleanup.
New jobs that failed before any apply are safe to clean without state; older
jobs without state require manual recovery. An older empty worker lock is
recoverable only for the sole failed job, with no running/pending/invalid jobs.
`.operation.lock` prevents concurrent cleanup/creation, including across workers.

Interrupted running/deleting jobs or a stale operation lock require manual
recovery, not an automatic retry. Before removing either lock, stop the worker, verify no Terraform process is
running, and inspect the private log and state; reconcile or destroy any
resources with Terraform using the same state and authorized credentials.
Do not delete the state directory. For manual recovery, use Terraform in the
individual deployment directory and verify removal in AWS. A worker restart
does not automatically retry jobs.
Configure access logging, CloudTrail data events and CloudWatch monitoring before
real use. This initial worker is not a production multi-tenant deployment service.

## Experimental authentication

The web app can use Cognito managed login with authorization code and PKCE.
A development user pool can be configured as described below. The private AWS
demo uses the same user pool with a separate web client; sign-in and sign-out
were tested with temporary accounts. Real email verification remains untested.
iOS and Android do not yet support Cognito sign-in. The default local
workspace is unauthenticated.
Do not expose it publicly or use it for real users.

For an isolated test, create a Cognito user pool and public app client with
managed login, authorization-code flow with PKCE, `openid` and `email` scopes,
token revocation, and no client secret. Register `<origin>/auth/callback` as
a callback URL and `<origin>/` as a sign-out URL. Enable email verification and
account recovery. Managed login v2 requires a branding style for each app client.

The local development pool uses a username and password with a verified email
for recovery. Cognito's default email delivery has development-scale limits;
production requires a reviewed email, MFA, and security configuration. AWS
resources may incur charges.

Set `COOKIST_AUTH_MODE=cognito` for core and gateway, and configure
`COOKIST_COGNITO_ISSUER` and `COOKIST_COGNITO_CLIENT_IDS` for core. Set
`VITE_AUTH_MODE=cognito`, `VITE_COGNITO_ISSUER`, `VITE_COGNITO_CLIENT_ID`, and
`VITE_COGNITO_DOMAIN` when building the web app. These identifiers are public
configuration, not secrets. Compose derives the web build's mode and issuer
from the core settings; rebuild the gateway image after changes. Use HTTPS
or localhost.

In Cognito mode, the gateway validates access through core before forwarding
API requests. Direct access to core must remain private. Health and application
assets remain accessible without signing in. The web app stores OAuth tokens in
per-tab session storage, where scripts can access them. Sign-out attempts
refresh-token revocation and ends the Cognito browser session, but independently
verified access tokens may remain valid until expiry. Protect against XSS and use
short access-token lifetimes.

## Validation

```powershell
yarn test
yarn build
Push-Location services\gateway; go test ./...; Pop-Location
.\scripts\gradle.ps1 -ProjectDir services\core test
.\scripts\test-env.ps1
```

See the native app READMEs for platform-specific setup. Building iOS requires
macOS and Xcode.

## Release notes

Install dependencies with `yarn install --frozen-lockfile`. Run `yarn changeset`
to record each change and commit the
resulting file in `.changeset`. Choose a major, minor, or patch bump according
to the change. The private `@cookist/web` workspace is the release-note unit for
the app; Changesets does not independently version the native apps or services.

Only when preparing a release, run `yarn changeset version` to consume pending
changesets and generate the workspace changelog. Review and commit the version
and changelog changes. This does not deploy the app or publish a package.
