# Cookist core

Minimal Kotlin service requiring Java 17. It has no database or catalog.

From the repository root in PowerShell:

```powershell
.\scripts\gradle.ps1 -ProjectDir services\core test installDist
.\services\core\run.ps1
```

Default listener: `127.0.0.1:8081`; configure `HOST` and `PORT`.

* `GET /health` returns `{"status":"ok"}` without authentication.
* `GET /api/me` requires a Cognito access token and returns only
  `{"actorId":"cognito:<issuer SHA-256>:<subject>"}`. The identifier is stable
  for the same issuer and subject. Responses are not cached.

Set `COOKIST_AUTH_MODE=cognito`, `COOKIST_COGNITO_ISSUER` to the Cognito user
pool issuer URL and `COOKIST_COGNITO_CLIENT_IDS` to a comma-separated list of
allowed app client IDs. Tokens are verified against the pool's JWKS, issuer,
expiry, token use and client ID. The default `demo` mode does not authenticate
or impersonate anyone; `/api/me` returns 401. Cognito settings without an
explicit auth mode fail startup.

Docker build context is the repository root:

```powershell
docker build -f services\core\Dockerfile -t cookist-core .
docker run --rm -p 127.0.0.1:8081:8081 cookist-core
```
