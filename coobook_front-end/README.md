# Cookbook frontend

The frontend builds to static HTML with Astro. React remains available for interactive features; ASTRO-01 keeps the existing application in a browser-only island while the Astro document shell, metadata, static fallbacks, root route, and email-confirmation route are introduced. No Astro server adapter or runtime is included.

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

`npm run build` runs Astro type checks and creates static output in `dist/`. With Astro's `file` format, the confirmation page is generated as `dist/auth/confirm.html`; the later static-host deployment must resolve `/auth/confirm?token=...` to this file without dropping the query. The Go API remains independent.

Existing `.jsx` application modules remain a documented ASTRO-01 transition and are scheduled for conversion in ASTRO-02. They are not covered by Astro's TypeScript checks; new TypeScript and Astro files use the strict Astro TypeScript preset.
