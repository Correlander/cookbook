# Optional Cloudflare static deployment

The frontend build emits `dist/_headers` from each page's Astro CSP meta policy. Publish that file with the static artifact so the root and `/auth/confirm` receive their matching hash policy. The header generator also emits `static-csp.json` and a Caddy include from the same validated data; do not hand-copy or edit hashes.

Keep static asset serving independent from the Go API. If a same-origin Cloudflare route is needed, use `worker.js.template` as a static-assets pass-through for `/api/*` and `/healthz` to a configured Go origin; preserve the incoming public Host/Origin, request body, cookies, response status, `Set-Cookie`, Go CSP and `Cache-Control`. The template uses a same-zone `resolveOverride` DNS alias so the public URL/Host remains intact. No application/business logic belongs in that proxy. Static paths should be served from assets, and failed/unknown API paths must never fall back to `index.html`.

This directory is a template/documentation only. Configure `GO_API_ORIGIN_HOST` as an HTTPS origin hostname reachable through a same-zone DNS name or same-zone CNAME alias. Configure no secrets in the static build. Do not deploy until the owner has reviewed domain, origin, access, and cost implications. This repository contains no account identifiers, credentials, DNS configuration, or production endpoint.

Cloudflare configuration and proxy resources are optional and are not created by the local Compose setup. The local integration proof uses the Caddy static host and Go API on one origin.
