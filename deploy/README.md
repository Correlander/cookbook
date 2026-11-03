# Static frontend and Go API deployment

## Local container layout

Compose builds two independent images:

- Root `Dockerfile` compiles and runs only the Go API. It does not copy Astro files or claim that the API serves the website.
- `deploy/Dockerfile.frontend` builds Astro static output, generates CSP headers from each built page's CSP meta, and packages only `dist/` with Caddy. Caddy resolves `/auth/confirm` from the standard `auth/confirm.html` file without consuming or dropping query parameters.

Caddy serves the browser origin and proxies `/api/*` plus `/healthz` to Go. It preserves the incoming public Host and Origin and passes Go response headers/status/body through. API errors and unknown API routes never fall back to the static root. Static HTML is `no-store`; only hashed `/_astro/` assets receive long-lived immutable caching. The root Compose port is loopback-only at 8080. Use `podman compose up --build` and stop with `podman compose down`; the named database volume persists.

## Frontend build and CSP

Use the frontend pinned Node version (currently `>=22.23.4`) and set `ASTRO_TELEMETRY_DISABLED=1` in restricted/read-only home environments. After Astro builds the standard `dist/index.html` and `dist/auth/confirm.html`, `scripts/generate-static-headers.mjs` validates the per-page Astro CSP meta and emits:

- `dist/_headers` for static asset hosting;
- `dist/static-csp.caddy` for the local Caddy host;
- `dist/static-csp.json` as a reviewable public policy manifest.

The generator reuses exact Astro SHA-256 script/style hashes and combines them with restrictive defaults. It fails on missing pages/policies/hashes, malformed hashes, unsupported or duplicate directives, unsafe inline/eval sources, event-handler attributes, and `javascript:` URLs. Keep API CSP owned by Go; never append static page CSP to API responses.

## Optional Cloudflare static hosting

`cloudflare/worker.js.template` is optional infrastructure-only pass-through for `/api/*` and `/healthz`; static requests use the Workers Static Assets binding. Replace `GO_API_ORIGIN_HOST` with an approved same-zone origin alias. Static API proxying is not needed for local Compose and no Worker/account resources are created here. See the Cloudflare README for route, origin, and review requirements. Do not deploy until domains, public-origin behavior, preview protection, and costs are reviewed.

## Browser integration tests

The Playwright runner lives under frontend-owned path `e2e/` and its task runner in `scripts/run-e2e.mjs`. It builds and serves the static artifact with the independent Go API, uses only private disposable data, captures magic-link email in a mode-600 temporary directory, and tears down owned processes/containers/data in `finally`. Use `npm run e2e`; never use the demo database or production endpoint.

For a bounded real Caddy/Go proxy smoke, pass existing frontend and backend image names explicitly: `npm run test:caddy -- --frontend-image IMAGE --backend-image IMAGE`. Podman is used by default; the runner uses `--pull=never`, creates uniquely labeled temporary containers/network, publishes Caddy on an ephemeral loopback port, mounts only an API `/data` tmpfs, and removes its owned resources on success, failure or signal. The Go mail command is `/bin/true`; requests use only a synthetic bogus token and never sign in or create a user. This smoke exercises the contents of the supplied images, so it does not prove the current checkout is present in images built earlier.

Add `--browser` to run the Playwright suite through that same Caddy origin. The published socket remains bound to `127.0.0.1`; the runner uses `http://localhost:<ephemeral-port>` as the public browser and RP origin for local WebAuthn. It uses the retained local Playwright CLI/browser, a private temporary email-capture directory, open registration for synthetic `route-user@example.test`, and the disposable `admin@example.test` bootstrap account. The suite runs with one worker so its captured sign-in link cannot race. Use optional `--browser-grep PATTERN` (up to 200 characters) to run a focused Playwright subset; omitting it runs the full suite. Browser artifacts and containers are removed after the run. Build the supplied images from the exact source revision you intend to validate; this mode does not build or pull them.

For a failure that needs inspection, optionally pass `--private-report-dir NEW_DIR`. The directory must not exist and its parent must already exist. The runner creates it with mode `0700` and copies a valid native Playwright JSON report up to 1 MiB to `playwright-report.json` with mode `0600`. The default keeps no report, and raw report contents are never printed. Missing, malformed or oversized reports do not produce a private copy and do not change the normal result summary or pass/fail gate. The caller owns this opt-in directory and must remove it in `finally` after inspection; keep it private because Playwright reports can contain page content and test data.
