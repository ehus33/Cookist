# Cookist

Cookist is being refocused on deployments. The repository currently contains a
neutral web and native workspace, a Go gateway, and a Kotlin authentication
service. An optional operator-only worker can create Terraform-managed sites.
It is disabled until explicitly configured.

The AWS demo uses the neutral workspace and experimental Cognito authentication.
Keep it private; it is not configured for production use.

The private hosted test now runs the deployment gateway and core together on
the existing EC2 worker, behind the Lightsail/Tailscale reverse proxy.
The gateway uses short-lived EC2 instance-role credentials with the scoped
test-site policy, not administrator SSO credentials or stored access keys.
Deployment access is limited to the configured operator and the four-site cap
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
or automatically free a slot after manual destruction.

Use one durable `COOKIST_DEPLOYMENT_ROOT` for all workers and users; separate
directories, installations, or Terraform runs outside the worker are not covered
by this cap. Keep state and deployment directories after cleanup rather than
deleting them to bypass the limit. This is a conservative test-phase creation
cap, not an AWS account-wide quota, spending limit, or automatic cleanup policy.
Deployment access remains restricted to the configured operator.

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
reads require that tag. The worker cannot update or delete distributions.
Do not assign the reserved tag to unrelated distributions.
CloudFront creation uses resource `*` where the API cannot resource-scope it.
Origin access controls cannot be tag-scoped: the worker can create and read them,
but cannot update or delete them. Use an authorized cleanup identity to destroy
the full site, including its origin access control, with the original state.

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

The worker runs one deployment at a time and persists its status. A failed
apply or interrupted process may leave billable resources and partial state.
The worker retains `.worker.lock` in those cases and refuses further deployments.
Before removing that lock, stop the worker, verify no Terraform process is
running, and inspect the private log and state; reconcile or destroy any
resources with Terraform using the same state and authorized credentials.
Do not delete the state directory. There is currently no destroy or retry
button: use Terraform in the individual deployment directory to destroy a site,
and verify removal in AWS. A worker restart does not automatically retry jobs.
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
