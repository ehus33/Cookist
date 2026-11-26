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

## Managed Terraform deployments

When enabled, **New deployment** starts a background Terraform apply for a
blank site or a simple business listing. The worker creates a **public**
CloudFront site backed by a private S3 bucket. Anyone with the URL can access
the site; it is not Tailscale-private. The template does not provide ordering,
payments, a database, or an editor for deployed sites. AWS charges continue
until the resources are destroyed; see [S3 pricing](https://aws.amazon.com/s3/pricing/)
and [CloudFront pricing](https://aws.amazon.com/cloudfront/pricing/).

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
5. Provision verified Cognito actors as `customer_admin`, `deployer` or `viewer`
   in the access policy, including their tenant quota. Self-registration does
   not create a tenant or grant checkout rights. Customer admins select
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

**Only the ingress is live.** The gateway still runs the earlier cleanup release,
not the Stripe billing build, so forwarded webhook POSTs currently return 401.
Finish creating the sandbox destination, configure its signing secret plus the
matching Stripe key and recurring price, and deploy the billing build before
expecting payment events to grant access. Do not treat a working HTTPS address
as a working subscription payment flow.

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
are literal data, not Terraform expressions. Both templates share the same
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
distributions require the reserved `CookistManaged=test-site` tag; distribution
reads, updates (needed to disable a distribution), and deletion require that tag.
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
