# Cookist

An original, local-first delivery platform prototype: customer discovery and checkout,
merchant fulfillment, courier jobs, simulated payments, and delivery tracking across
React web, native SwiftUI iOS, and native Kotlin/Compose Android.

This recreates the **kind of search flow observed with REA**, not DoorDash's source,
branding, assets, private APIs, ranking, or production infrastructure. Search ranking,
API contracts, tax rules, and the design here are our own implementations.

**Demo only:** by default identities are selectable without passwords, money never
moves, and courier locations are manually simulated. Do not expose this service publicly or use
real customer/card data. Changing the delivery address does not change the seeded
Seattle catalog, illustrative distances, or travel estimates.

### Experimental authentication backend

The `feature/signIn` work is incomplete: no client can sign in with Cognito yet,
and no Cognito user pool is provisioned. Do not enable this mode on the running
demo or expose it publicly.

The core service can be started for backend testing with `COOKIST_AUTH_MODE=cognito`,
`COOKIST_COGNITO_ISSUER=https://cognito-idp.<region>.amazonaws.com/<pool-id>`,
and `COOKIST_COGNITO_CLIENT_IDS` set to a comma-separated list of public app
client IDs. The service validates Cognito access tokens. Users default to customers;
the trusted Cognito groups `cookist-courier` and `cookist-merchant-<store-id>`
grant courier or a single store's merchant access. Assign these groups only through
trusted administration, never through self-service. Conflicting roles are rejected.
Changes to group membership take effect when the access token expires.

The service disables demo session creation and demo accounts in Cognito mode.
Each database is bound to one authentication mode and, for Cognito, one user
pool. Use a new database for Cognito; switching a demo database or user pool is
rejected. Previously created demo and experimental Cognito orders are not
automatically migrated or linked. The default `demo` mode remains
unchanged and is not authentication. Do not consider this backend mode production-ready.

The experimental web courier controls require a verified Cognito courier and an
explicit click before requesting browser location permission. Sharing stops when
the page is hidden or the delivery ends. Customers and the assigned merchant can
see the current position. Location is kept only in the core process's memory,
becomes unavailable after 60 seconds without an update, and is removed by a
periodic cleanup within another 30 seconds; restarting the service clears it.
This is single-instance behavior, not a production-ready distributed tracker.
Web Cognito sign-in and native location sharing are not yet implemented, so this
flow cannot yet be used end to end by app users.

## Architecture

```text
React web / SwiftUI iOS / Kotlin Compose Android
                         |
                    Go gateway :8080
                     /             \
         Kotlin commerce :8081    Python search :8082
                |                       |
              SQLite <--- fresh catalog from commerce
```

| Component | Responsibility |
|---|---|
| `services/core` | Kotlin/JVM + SQLite: catalog, persistent demo sessions, order state machine, merchant availability, courier assignment, payment/refund ledger, tracking events |
| `services/search` | Python standard-library HTTP service: mixed query/store autocomplete, dish/cuisine/name matching, filters, deterministic sorting |
| `services/gateway` | Go standard-library gateway: routing, origin policy, readiness checks, React static hosting |
| `apps/web` | TypeScript + React + Vite: responsive customer, merchant, courier workspaces |
| `apps/ios` | Native SwiftUI client; see its README for Xcode setup |
| `apps/android` | Native Kotlin + Compose client; see its README for build/device setup |
| `shared` | Shared API contract and original seeded sample catalog |

## Run on Windows

Requires Node.js 22.12+ (or 24+), npm, JDK 17, Python 3.11+, Go 1.24+, and
internet for the first dependency/Gradle restore. Use a PowerShell terminal:

```powershell
.\scripts\start.ps1
```

Open **http://localhost:8080**. The first run builds the web app, downloads a local
Gradle distribution if needed, builds Kotlin, and builds Go. Subsequent runs:

```powershell
.\scripts\start.ps1 -SkipBuild
```

The script refuses to take over occupied ports and stops its own child processes on
Ctrl+C. No database or browser data is deleted. Core state persists in
`.data/cookist.db`; web bags and the selected demo identity persist in browser storage.

For frontend hot reload, leave the stack running and run `npm run dev` in a second
terminal. Open http://localhost:5173; Vite proxies `/api` to the gateway.

### Environment and planned integrations

The root `.env` is local and Git-ignored. A safe, blank-secret template is tracked
as [.env.example](.env.example). If your checkout has no `.env`, create one once:

```powershell
Copy-Item .env.example .env
```

Do not overwrite an existing populated `.env`. Edit credentials locally, never in
chat, source code, screenshots, or commits. The stack launcher loads root `.env`
before building or starting services. Existing process variables take precedence;
blank placeholders are left unset. Restart the stack to apply changes. This does
not modify already running services.

**Configuration only:** the integration fields below are reserved for later
implementation, except that the web order tracker now uses OpenFreeMap's public
Liberty style directly. The `OPENFREEMAP_STYLE_URL` environment variable is not
read by the web app. Setting another URL or key does not enable a provider.
Demo payments, identities, address handling, and simulated location updates remain
unchanged. Native apps are unchanged. No provider has been chosen for the other
optional services.

| Integration | Reserved variables | Next implementation needed |
|---|---|---|
| OpenFreeMap | `OPENFREEMAP_STYLE_URL` (reserved, not read) | Web tracker uses MapLibre and the public Liberty style; native maps are not yet implemented |
| Stripe | `STRIPE_PUBLISHABLE_KEY`, `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` | Test-mode checkout, server totals, signed webhooks, idempotency, refunds |
| Address autocomplete/geocoding | `GEOCODING_PROVIDER`, `GEOCODING_BASE_URL`, `GEOCODING_API_KEY` | Select an authorized provider, address validation and location-aware catalog |
| Driving routes/ETA | `ROUTING_PROVIDER`, `ROUTING_BASE_URL`, `ROUTING_API_KEY` | Routing provider, travel estimates, real destination coordinates |
| Email | `EMAIL_PROVIDER`, `EMAIL_API_KEY`, `EMAIL_FROM` | Transactional provider, sender verification and delivery retries |
| SMS | `SMS_PROVIDER`, `SMS_API_KEY`, `SMS_FROM` | Provider, opt-in and delivery handling |
| Push | `PUSH_PROVIDER`, `PUSH_CREDENTIALS_FILE` | Server credentials file outside Git, device registration and platform setup |
| Production auth | `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_AUDIENCE` | Reserved only; the experimental Cognito backend uses the separate `COOKIST_COGNITO_*` settings above |

OpenFreeMap supplies tiles/styles, not address search, routing, or location
collection. Web browsers request map data directly from OpenFreeMap, which can
receive their IP addresses and the map area being viewed, including an approximate
area around any live position displayed. Demo kitchens and simulated locations
are shown in demo mode. The entered delivery address is not geocoded or sent to
OpenFreeMap. Attribution is shown
on the map. iOS and Android do not yet include maps.

The web discovery page also has an opt-in King County food-establishment directory
map based on a scraped dataset. Listings are not verified Cookist partners, may be outdated, and cannot be ordered
from. Directory search is separate from the seeded demo catalog search. The map
loads its data only when opened; map tiles are requested from OpenFreeMap, not the
directory records themselves.

On the directory map, **Use my location** requests browser permission and shows
the current position locally. Location updates stop when the user stops them,
closes the map, or hides/leaves the page; the position is not saved or sent to the
Cookist backend. OpenFreeMap can infer the approximate area from tile requests.
Location requires HTTPS (or localhost), browser/device support, and permission;
if permission was previously denied, change it in the browser's site settings.
This does not change the delivery address or demo restaurant search.

The map uses a checked-in public GeoJSON file containing only names, addresses,
cities, ZIP codes, and coordinates; phone and other source fields are omitted.
The original scrape at `data/king_county_food_establishments.json` is ignored by
Git and excluded from container builds. To refresh the public file from a local
scrape, run `npm run prepare-directory --workspace @cookist/web` and review the
result before committing. The original scrape is not required for normal builds.
Check the source's provenance, redistribution terms, and accuracy before publishing
the directory beyond the private demo.
Use **Stripe test/sandbox credentials only** during development; do not add live
keys to this demo. Production authentication must replace identity switching
before enabling real charges. Different web/native OIDC clients may need separate
client IDs when that integration is implemented.

Keep all secret keys, webhook signing secrets, OIDC client secrets, and push
credentials server-side. Never prefix secrets with `VITE_`: those variables are
public and may be embedded in browser assets. The root `.env` is intentionally
not configured as Vite's environment directory; maps/public Stripe settings are
not yet exposed to the web app. Never embed server credentials in mobile builds.
The loader passes process variables to child processes, so only run trusted local
builds with populated credentials. Git-ignore is not encryption or secret storage;
use a deployment secret manager for production.

The local parser supports `KEY=value`, single/double outer quotes, blank lines,
and full-line `#` comments. Values are literal: no shell execution, interpolation,
escape processing, multiline values, or inline comments. Duplicate keys, malformed
assignments, and unclosed quotes fail before any file values are applied; errors
do not print values. Use simple literal values without `$` expansion if also
using Docker Compose, whose environment syntax has different expansion rules.

For manual service commands, load the file in **each** PowerShell terminal first:

```powershell
. .\scripts\Import-CookistEnv.ps1
Import-CookistEnv -Path (Join-Path $PWD '.env')
```

Compose reads root `.env` for interpolation, but does **not** automatically inject
it into containers. Current Compose settings remain explicit and unchanged;
reserved integration credentials are not forwarded until a consuming integration
is implemented. Container SQLite/catalog paths remain the container-specific paths
in `compose.yaml`. Environment files are excluded from Docker build contexts.

### Containers

With Docker Engine running:

```powershell
docker compose up --build
```

Open http://localhost:8080. SQLite persists in the `cookist-data` named volume.
Only the gateway is published, bound to host loopback. `docker compose down` stops
the services without deleting that volume.

### Individual services

Run each command from the repository root, in a separate terminal, after building:

```powershell
.\services\core\run.ps1
python .\services\search\search.py
.\.tools\cookist-gateway.exe
```

The default bind address is loopback. Environment variables:

| Service | Variables |
|---|---|
| All | `HOST`, `PORT` |
| Kotlin | `COOKIST_DB`, `COOKIST_CATALOG` |
| Python | `CORE_URL` |
| Go | `CORE_URL`, `SEARCH_URL`, `WEB_DIST`, comma-separated `ALLOWED_ORIGINS` |

Default origin allowlist: `http://localhost:8080`, `http://127.0.0.1:8080`,
`http://localhost:5173`, `http://127.0.0.1:5173`. Native clients do not send a web
Origin header. Android emulator uses `http://10.0.2.2:8080`; `adb reverse tcp:8080
tcp:8080` with `http://127.0.0.1:8080` is an alternative. Keep any non-loopback
binding restricted to your trusted local development network.

## Try the complete delivery flow

1. **Customer:** browse as a guest, search `piz` or `miso`, try filters, and open a
   kitchen. Add items to your bag. Choose demo customer **Alex** or **Sam**.
2. **Checkout:** enter a sample delivery address. Choose the successful or declined
   demo card. A decline creates no order. A successful order uses server-calculated
   prices, 10% demo tax, the kitchen's delivery fee, and a $1.49 service fee.
3. **Merchant:** switch workspace, select the matching kitchen identity, then
   **Accept order → Start cooking → Mark ready**. Open/close the kitchen or toggle
   menu items as sold out. Other merchants cannot access the order.
4. **Courier:** select Riley or Jordan, claim the ready delivery, confirm pickup,
   manually send simulated positions, and complete delivery. Only one courier can
   claim a job. Manual destination coordinates are illustrative, not geocoded.
5. **Customer:** return to the same customer identity to see status history, courier
   assignment, and the interactive web map. Web updates every four seconds.
   Kitchen and courier positions are demo data, not live GPS or an actual route.
6. To test refunds, create another order and cancel it while pending. Merchants may
   cancel before courier pickup. The simulated payment is marked refunded.

Each demo identity has its own order visibility. Browser bags are separate per
identity. Checkout retries reuse an idempotency key; reusing a key with changed
content is rejected. Historical order prices are snapshots, not current menu prices.

## API

The language-neutral source of truth is [shared/api-contract.json](shared/api-contract.json).
Errors use `{ "error": { "code": "...", "message": "..." } }`.
Protected routes take `Authorization: Bearer <demo-session-token>`.

```powershell
Invoke-RestMethod http://localhost:8080/api/search?q=pizza
$session = Invoke-RestMethod http://localhost:8080/api/sessions -Method Post `
  -ContentType application/json -Body '{"role":"customer","actorId":"customer-1"}'
Invoke-RestMethod http://localhost:8080/api/orders `
  -Headers @{ Authorization = "Bearer $($session.token)" }
```

Search queries are bounded, matched against store names, cuisines, tags, and available
menu items. Autocomplete combines query terms and stores. Ranking is a transparent
local weighted match, not a reconstruction of DoorDash ranking. Search fetches the
current commerce catalog on each request; an upstream outage returns 502, not an
empty success response.

## Validation

```powershell
npm run build
npm test
python -m unittest discover -s services\search -v
Push-Location services\gateway; go test ./...; Pop-Location
.\scripts\gradle.ps1 -ProjectDir services\core test
.\scripts\test-env.ps1
```

With the stack running, exercise the real three-service delivery flow:

```powershell
python .\scripts\smoke.py
```

The smoke test writes sample orders to the running database and restores any
availability flags it changes. Set `COOKIST_DB` to a separate file before starting
the stack if you want isolated test state. Native build commands are in each app's
README. iOS requires macOS and Xcode; it cannot be compiled on Windows.

## Deliberate production gaps

This is a working local full-flow prototype, **not a production delivery business**.
It does not include verified authentication, onboarding/KYC, real payment processing
or PCI handling, production-ready location tracking/routing, native maps, push/SMS, real inventory reconciliation,
tax jurisdiction handling, payouts, dispatch optimization, operational support,
or production deployment hardening. Implement those against authorized providers
before real-world use. Do not interpret seeded ratings/ETAs or simulated map
positions as live facts.
