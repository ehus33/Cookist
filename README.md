# Cookist

Cookist is being refocused on deployments. The repository currently contains a
neutral web and native workspace, a Go gateway, and a Kotlin authentication
service. Deployment-management functionality and Terraform are not yet implemented.

The existing AWS demo is unchanged. Keep it private: the new workspace and its
experimental authentication have not been deployed or verified end to end.

## Run on Windows

Requires Node.js 22.12+ (or 24+), npm, JDK 17, and Go 1.24+. Run:

```powershell
.\scripts\start.ps1
```

Open http://localhost:8080. For a frontend development server, run
`npm run dev` in another terminal. Vite proxies API requests to the gateway.
Copy [.env.example](.env.example) to `.env` for optional local configuration;
do not commit populated secrets.

For containers, run `docker compose up --build` and open
http://localhost:8080. The gateway binds to loopback.

## Experimental authentication

The web app can use Cognito managed login with authorization code and PKCE.
Provisioning and end-to-end testing remain pending; iOS and Android do not yet
support Cognito sign-in. The default local workspace is unauthenticated.
Do not expose it publicly or use it for real users.

For an isolated test, create a Cognito user pool and public app client with
managed login, authorization-code flow with PKCE, `openid` and `email` scopes,
token revocation, and no client secret. Register `<origin>/auth/callback` as
a callback URL and `<origin>/` as a sign-out URL. Enable email verification and
account recovery. Managed login v2 requires a branding style for each app client.

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
npm test
npm run build
Push-Location services\gateway; go test ./...; Pop-Location
.\scripts\gradle.ps1 -ProjectDir services\core test
.\scripts\test-env.ps1
```

See the native app READMEs for platform-specific setup. Building iOS requires
macOS and Xcode.
