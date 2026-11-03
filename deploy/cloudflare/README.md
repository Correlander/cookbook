# Optional Cloudflare static deployment

The frontend build emits `dist/_headers` from each page's Astro CSP meta policy. Publish that file with the static artifact so every canonical page route and its `.html` alias receive that page's matching CSP, `Cache-Control: no-store`, and the shared `Referrer-Policy: no-referrer`. The header generator also emits `static-csp.json` and a Caddy include from the same validated data; do not hand-copy or edit hashes.

Keep static asset serving independent from the Go API. If a same-origin Cloudflare route is needed, use `worker.js.template` as an infrastructure-only API proxy and static document router. API and health requests are handled first and preserve the incoming public Host/Origin, request body, cookies, response status, `Set-Cookie`, Go CSP and `Cache-Control`. The template uses a same-zone `resolveOverride` DNS alias so the public URL/Host remains intact. It maps extensionless generated document routes, including `/creator` and `/account/creator-profile`, to their exact `.html` assets while retaining the query. The `.html` aliases are served directly. Configure the Workers Static Assets binding with `html_handling = "none"` so Cloudflare does not redirect `.html` requests before their generated `_headers` policy applies; see Cloudflare's [HTML handling reference](https://developers.cloudflare.com/workers/static-assets/routing/advanced/html-handling/). Keep SPA fallback disabled so unknown paths do not resolve to the root document. The `ASSETS.fetch` responses continue through the generated static header rules. No application/business logic belongs in that proxy.

Use this setting in the deployment's Worker configuration (the repository provides only a template, not a Wrangler configuration):

```toml
[assets]
directory = "./dist"
binding = "ASSETS"
html_handling = "none"
```

This directory is a template/documentation only. Configure `GO_API_ORIGIN_HOST` as an HTTPS origin hostname reachable through a same-zone DNS name or same-zone CNAME alias. Configure no secrets in the static build. Do not deploy until the owner has reviewed domain, origin, access, and cost implications. This repository contains no account identifiers, credentials, DNS configuration, or production endpoint.

Cloudflare configuration and proxy resources are optional and are not created by the local Compose setup. The local integration proof uses the Caddy static host and Go API on one origin.
