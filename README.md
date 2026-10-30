# Cookbook

A self-hostable cookbook for collecting recipes and organizing them into cookbooks. The current application includes a Go API backed by SQLite and a React/Vite discovery interface. The discovery page reads explicitly public recipes from the instance. Signed-in users can create private recipes and cookbooks, add recipes to their own cookbooks, and browse recipes and cookbooks they own or can access.

## Requirements

- Go 1.23 or newer
- Node.js 20 or newer and npm
- A mail delivery command that accepts a JSON object on stdin (for email sign-in)

## Local development

1. Copy `cookbook_back-end/.env.example` to `cookbook_back-end/.env` and configure the database path, public URL, registration policy, and mail command. The backend reads environment variables directly; load the file in your shell or use your preferred environment loader.
2. Start the API:

   ```sh
   cd cookbook_back-end
   go run .
   ```

   It listens on `127.0.0.1:8000` by default and initializes the SQLite schema at the configured database path.
3. In another terminal, start the frontend:

   ```sh
   cd coobook_front-end
   npm ci
   npm run dev
   ```

   Vite proxies `/api` requests to the local API.

The initial instance is private, registration is invite-only, and courses and federation are disabled. Set `COOKBOOK_REGISTRATION=open` only when you intend to allow anyone to create an account. Invitation management is not implemented yet, so invite-only currently means only accounts provisioned by an administrator can sign in.

## Email command contract

`COOKBOOK_EMAIL_COMMAND` names an executable. For each sign-in request, Cookbook writes one JSON object to its stdin:

```json
{"to":"person@example.test","subject":"Your Cookbook sign-in link","text":"Use this one-time link to sign in. It expires in 15 minutes.","sign_in_url":"https://cookbook.example/auth/confirm?token=..."}
```

The command should deliver the message and exit with status 0 on success. It must not log the one-time URL. The link expires after 15 minutes and can only be used once. Use HTTPS for `COOKBOOK_PUBLIC_URL` outside local development and terminate TLS at a trusted reverse proxy. Keep the API bound to loopback behind that proxy.

## Current scope

Email-link authentication, durable sessions, private-first SQLite schema, access checks, structured recipe/cookbook APIs, public recipe browsing, and private recipe creation are present. Passkeys, invitation administration, recipe revision history, media uploads, and federation are not implemented. Federation is disabled by default; protocol evaluation and design are tracked in `AUDIT_AND_PLAN.md`.
