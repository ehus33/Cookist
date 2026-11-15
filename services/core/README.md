# Cookist core

Java 17, Kotlin and SQLite. Demo authentication and payments only; no external services.
From the repository root in PowerShell:

```powershell
.\scripts\gradle.ps1 -ProjectDir services\core test installDist
.\services\core\run.ps1
```

On Windows, stop a running core before `installDist`: Java holds its installed JARs open.

Default listener: `127.0.0.1:8081`. Configure `HOST`, `PORT`, `COOKIST_DB`
(default `.data/cookist.db`) and `COOKIST_CATALOG` (default `shared/catalog.json`).
Relative paths resolve from the process working directory, so run from the repo root.
Startup reads the SQLite catalog and warms catalog/account/health JSON encoding before
binding the listener; the listening message means those paths are ready. Responses use
Jackson's tree/collection serializers without Kotlin reflection. The catalog is still
read fresh per request, so merchant availability changes are immediately visible.
Catalog records are inserted only if absent; availability, orders, sessions and checkout
idempotency survive restarts. Stop the service before deleting the database to reset the demo.

All routes and JSON follow `shared/api-contract.json`; search is a separate service.
Order creation returns 201, identical checkout replays 200, changed payloads 409.
Idempotency is scoped per customer; addresses are trimmed before comparison.
Declined payment results are also persisted, return 402 on replay, and create no order.
Unknown JSON fields (including client prices), duplicate items/keys, malformed JSON,
out-of-range values and bodies above 32 KiB are rejected. Inaccessible orders return 404;
wrong-role actions return 403; invalid transitions and unavailable claims return 409.
Claims use a SQLite immediate transaction, serializing concurrent claims across processes.
An identical replay returns its original immutable checkout response, not current order state.

Cold-start readiness regression (launches fresh JVMs against new and existing SQLite
databases, with real HTTP requests capped at the downstream two-second deadline):

```powershell
.\scripts\gradle.ps1 -ProjectDir services\core test --tests cookist.ColdStartTest
```

Docker build context is the repository root:

```powershell
docker build -f services\core\Dockerfile -t cookist-core .
docker run --rm -p 127.0.0.1:8081:8081 -v cookist-data:/app/data cookist-core
```
