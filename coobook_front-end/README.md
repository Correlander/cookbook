# Cookbook frontend

The frontend builds to static HTML with Astro. Astro owns the document, metadata, app shell, and footer. The React application is rendered into the static pages and hydrated on load; infrequent admin, course, federation, cooking, passkey, and editor features load as separate chunks when needed. User-specific API data is fetched in the browser and is not baked into the build. No Astro server adapter or runtime is included.

## Requirements

- Node.js `22.23.4` or newer (see `.nvmrc`)
- npm

## Development

Run the Go API from `cookbook_back-end` on `127.0.0.1:8000`, then start the frontend:

```sh
npm ci
npm run dev
```

Astro serves the UI at `http://localhost:4321` and proxies `/api/*` requests to the Go service.

## Checks and production output

```sh
npm test
npm run lint
npm run build
npm audit
```

`npm run build` runs strict Astro type checks and creates static output in `dist/`. The confirmation page is generated as `dist/auth/confirm.html`; the static host resolves `/auth/confirm?token=...` to this file without dropping the query. The Go API remains independent.

`npm run e2e` builds the frontend and runs the isolated API/static-host browser flow. It requires the managed Playwright browser installed by the project tooling workflow. `npm run test:ops` runs static header generation and API proxy contract tests; these are also part of `npm test`.
