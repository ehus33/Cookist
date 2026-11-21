# Cookist

Cookist is being refocused on deployments. The repository currently contains a
neutral web and native workspace, a Go gateway, and a Kotlin authentication
service. Deployment-management functionality and Terraform are not yet implemented.

The AWS demo uses the neutral workspace and experimental Cognito authentication.
Keep it private; it is not configured for production use.

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
