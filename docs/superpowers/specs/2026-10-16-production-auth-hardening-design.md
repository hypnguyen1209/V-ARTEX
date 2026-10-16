# V-ARTEX Production Authentication Hardening Design

**Date:** 2026-10-16

**Status:** Approved for implementation planning

## Context

V-ARTEX currently exposes a first-run password initialization endpoint without a bootstrap secret, binds the native HTTP server and Docker host port to every interface by default, permits every browser origin through CORS, and signs seven-day JWTs with a key that does not change when the administrator password changes. A reachable uninitialized instance can therefore be claimed by the first client, and a stolen token remains valid after a password reset.

This design ports the proven authentication hardening behavior from `artex-global` into the English-only V-ARTEX architecture. It preserves the existing single-administrator model, PostgreSQL password storage, JWT authentication, seven-day token lifetime, and current English-only migration behavior. It does not introduce server-side sessions or a new database schema.

Production hardening is split into independent projects:

1. This specification: secure bootstrap, session revocation, network boundaries, frontend integration, deployment defaults, and their regression tests.
2. A later specification: CI and release hardening, including mandatory Go, PostgreSQL, frontend, documentation, and release gates.

## Goals

- Prevent an unauthenticated remote client from claiming a new installation.
- Make native and Docker deployments local-only unless the operator explicitly exposes them.
- Revoke all existing sessions when the administrator password changes, including out-of-process password resets.
- Preserve already initialized passwords and all existing application data.
- Fail closed when PostgreSQL cannot confirm authentication state.
- Restrict browser cross-origin access to explicitly allowed origins.
- Keep the implementation compatible with V-ARTEX's existing single-admin, English-only product model.

## Non-goals

- Multiple users, roles, or per-device session management.
- Refresh tokens, server-side session tables, or token revocation lists.
- Built-in TLS termination. Public deployments must use an HTTPS reverse proxy.
- Trusting proxy-provided client IP headers.
- Changing the seven-day JWT lifetime.
- Retaining compatibility with clients that call `POST /api/auth/init` without a setup token.
- CI and release workflow changes, which belong to the follow-up hardening specification.

## Architecture

Authentication remains owned by `server/auth.go`. The server continues storing the administrator bcrypt hash under `auth.password_hash` and storing a local JWT root key outside the browsable workspace. The hardened server adds four bounded responsibilities:

1. First-run bootstrap through a console or environment setup token, with optional headless password initialization.
2. JWT key derivation from both the root key and the current password hash.
3. Per-peer authentication failure throttling.
4. Exact-origin CORS enforcement at the HTTP middleware boundary.

The frontend setup page and change-password dialog consume the new API contracts. Native and Docker entry points enforce local-only defaults. No other application subsystem needs to know about setup tokens or password-derived signing keys.

## Authentication State and Bootstrap

At server startup, authentication is in one of three states:

### Initialized

PostgreSQL contains a non-empty `auth.password_hash`. The server caches the hash, ignores `ARTEX_ADMIN_PASSWORD` and `ARTEX_SETUP_TOKEN`, and serves the login page. Environment variables must never overwrite an existing password.

### Headless bootstrap

PostgreSQL has no password and `ARTEX_ADMIN_PASSWORD` is non-empty. The server validates the password with the same rules as the HTTP API, hashes it with bcrypt, and writes it with `InsertSettingIfAbsent`. A competing server instance or request may win the insert; the losing process reloads the stored hash and does not overwrite it.

If the environment password fails validation, the server logs the reason without logging the password and falls back to setup-token bootstrap. Hash-generation or database-write failures remain fail-closed: startup does not issue a setup token or serve an ambiguous authentication state.

### Interactive bootstrap

PostgreSQL has no password and headless initialization did not succeed. If `ARTEX_SETUP_TOKEN` is non-empty after trimming, that exact value becomes the setup token and is never printed. Otherwise the server generates a cryptographically random 32-byte token, stores it only in memory, and prints it once with an `[auth]` prefix.

The token is never returned by an API. Token comparison is constant-time. Successful password creation clears the in-memory token. A mutex serializes in-process initialization, while `InsertSettingIfAbsent` remains the cross-process source of truth.

If PostgreSQL cannot determine whether a password exists, the server neither initializes from the environment nor creates an externally usable setup state. Status and authentication requests fail closed.

## API Contracts

### Authentication status

`GET /api/auth/status` returns:

```json
{
  "initialized": false,
  "setup_token_required": true
}
```

`setup_token_required` is true exactly when no administrator password exists. A database read failure returns HTTP 503 rather than `initialized: false`.

### Password initialization

`POST /api/auth/init` requires:

```json
{
  "password": "new password",
  "setup_token": "token from console or environment"
}
```

Missing or malformed input returns 400. A wrong token returns 403 and records a failed authentication attempt. An already initialized installation returns 403. A throttled client receives 429. A database availability failure returns 503. Successful initialization returns a JWT signed with the password-bound key.

There is no compatibility path that accepts only `password`.

### Password change

`POST /api/auth/change-password` continues requiring a valid current JWT, the current password, and the new password. On success it stores the new bcrypt hash, refreshes the server's hash cache, invalidates all previously issued JWTs, and returns:

```json
{
  "ok": true,
  "token": "new JWT for the current browser"
}
```

The frontend must store the replacement token before closing the dialog. If it cannot store the token, it clears local authentication state and routes to `/login`.

## Password-bound JWTs

The effective HS256 signing key is:

```text
HMAC-SHA256(root JWT key, current bcrypt password hash)
```

The server caches the current password hash for at most two seconds. Token verification fails closed if the hash cannot be read. When a token fails against the cached hash, the server reloads the hash once and retries only if the value changed. This makes changes made through `reset-password.sh` or a direct database operation revoke old sessions within two seconds without requiring a restart.

The seven-day token lifetime remains unchanged. Password changes return a fresh token so the browser performing the change can continue while every other session is logged out.

## JWT Root-key Migration and Rollback Safety

The first hardened startup rotates the legacy unversioned `jwt.key` and writes a versioned key format atomically. New installations create the versioned format directly. Hardened binaries parse and use the random key payload; older binaries treat the full versioned file as an opaque key, which they already support because they only require at least 32 bytes.

This rotation guarantees that pre-hardening JWTs do not become valid again if self-update rolls the binary back. A crash before the atomic rename leaves the legacy file in place, so the next hardened startup retries the rotation. A crash after the rename leaves a complete versioned file, so the next startup reuses it. Neither case can restore an older signing key. Failure to write the replacement key aborts startup rather than continuing with ambiguous token state.

The migration changes no database schema and intentionally signs every user out once.

## Authentication Rate Limiting

One in-memory limiter is shared by initialization, login, and password-change failures. Records are keyed by the TCP peer address, not `X-Forwarded-For`:

- Five failed attempts are allowed within a ten-minute window.
- Further failures start at a 30-second cooldown.
- Each subsequent failure doubles the cooldown, capped at ten minutes.
- A successful authentication operation clears the client record.
- Stale records expire after ten minutes.
- The map is bounded at 10,000 client records.

`X-Forwarded-For` may appear in audit logs but never controls enforcement because it is client-supplied unless a trusted-proxy system exists. Multiple users behind the same reverse proxy therefore share a limiter key. This is acceptable for the current single-administrator system and safer than accepting spoofable headers.

## Network Boundary and CORS

### Native process

The default HTTP address changes from `:8787` to `127.0.0.1:8787`. Development mode also binds the backend to `127.0.0.1`. Remote access requires the operator to pass an explicit address such as `-addr :8787`.

If the server is explicitly bound to a non-loopback address while the administrator password is unset, it emits a prominent warning. The setup token still prevents account takeover, but the warning directs the operator to finish setup before exposure.

### Docker Compose

The process inside the container continues listening on `:8787`. Host publishing becomes:

```yaml
ports:
  - "${ARTEX_BIND:-127.0.0.1}:8787:8787"
  - "127.0.0.1:8788:8788"
```

`ARTEX_BIND` controls only Docker host publishing. Setting `ARTEX_BIND=0.0.0.0` is an explicit decision to expose the service.

### CORS

Same-origin embedded UI requests need no CORS header. By default, cross-origin requests are accepted only from the local development frontend at `http://localhost:5173` and `http://127.0.0.1:5173`.

`ARTEX_CORS_ORIGINS` replaces that default with a comma-separated set of exact origins. Origins are normalized for case and surrounding whitespace. Wildcard `*` is not supported. Allowed responses echo the exact origin and include `Vary: Origin`. Disallowed preflight requests return 403; allowed preflight requests return 204 with the supported methods and headers.

## Frontend Behavior

The `/setup` page adds a required setup-token field before the password fields. The input disables autocomplete and spell checking. Help text tells Docker users to inspect `docker compose logs artex` and tells managed deployments to use the configured setup token.

The page checks authentication status before rendering the form:

- Initialized installations redirect to `/login`.
- Uninitialized installations display the token and password fields.
- Status failures display an unavailable state and a retry action. They never render an initialization form.

The API client changes `initPassword` to accept and send the setup token, and changes `changePassword` to return the replacement token. The change-password dialog stores that token and explains that other sessions are logged out.

## Configuration and Documentation

Docker Compose passes these values into the application container:

- `ARTEX_SETUP_TOKEN`
- `ARTEX_ADMIN_PASSWORD`
- `ARTEX_CORS_ORIGINS`

`ARTEX_BIND` remains a Compose interpolation value for host publishing.

`.env.example` documents local-only defaults, interactive setup, headless setup, exact CORS origins, and the bootstrap-only behavior of `ARTEX_ADMIN_PASSWORD`. Operators should remove a plaintext bootstrap password after initialization or inject it through their deployment secret mechanism.

The README documents how to find the generated token, how to perform headless bootstrap, and how to expose the service behind an HTTPS reverse proxy. Examples contain no real secrets. Documentation explicitly warns operators to finish setup before enabling remote access.

## Error Handling and Logging

- Database read errors never become an uninitialized state.
- Setup-token generation failures produce no matchable empty token.
- Secret values from environment variables are never logged.
- A generated setup token is logged once because the console is its intended delivery channel.
- Authentication failures log the TCP peer and optionally display `X-Forwarded-For` as untrusted context.
- Key migration failures abort startup.
- The API returns stable English error messages and appropriate 400, 401, 403, 429, 500, or 503 status codes.

## Testing Strategy

### Backend unit tests

- Password hashes produce different signing keys.
- Password changes invalidate old tokens and accept fresh tokens.
- External password changes take effect within the cache bound.
- Environment and random setup-token paths do not leak configured secrets.
- Random tokens are generated and logged once.
- Limiter thresholds, cooldown growth, expiry, reset, and bounded storage behave as specified.
- Forwarded headers do not affect the limiter key.
- Loopback detection covers IPv4, IPv6, hostname, wildcard, and port-only addresses.
- CORS accepts exact origins, rejects others, sets `Vary`, and rejects wildcard configuration.
- Versioned root-key migration rotates legacy keys once and prevents legacy-token revival under the old verification behavior.

### PostgreSQL integration tests

- Missing or wrong setup tokens cannot create a password.
- A valid setup token initializes the password exactly once.
- Concurrent initialization requests produce one winner.
- Headless bootstrap initializes an empty database but never overwrites an existing password.
- Invalid or overlong environment passwords are not stored.
- Authentication status and token verification fail closed on data-source errors.
- Login and password change are throttled.
- Password change returns a fresh token and invalidates the old token.

### Frontend and deployment checks

- A Node regression test verifies that initialization sends `setup_token`.
- A Node regression test verifies that password change stores the replacement token.
- A regression test verifies that status failure cannot display the setup form.
- `npm run build:static` succeeds.
- `docker compose config` shows `127.0.0.1:8787` as the default host mapping.
- A command-package test locks the native default address.

### Full regression

- Run all Go tests, including PostgreSQL-backed authentication tests.
- Run all existing frontend Node tests and the static frontend build.
- Confirm the existing English-only invariant and legacy-data migration tests still pass.

## Acceptance Criteria

- A missing or incorrect setup token cannot initialize an administrator password.
- A correct token can initialize the password only once.
- An existing password is never changed by bootstrap environment variables.
- All sessions issued before this release are invalid after the one-time key migration.
- Password changes and resets invalidate prior sessions within two seconds.
- Native and Docker defaults accept local connections only.
- Cross-origin browser calls succeed only for exact allowed origins.
- No configured secret is returned by an API or printed in logs.
- Backend, PostgreSQL, frontend, deployment, English-only, and migration regression checks pass.

## Rollout and Recovery

The release notes must announce the one-time logout and the new required setup token for uninitialized instances. Existing installations keep their password and data. New installations retrieve the token from the console or provide bootstrap environment values.

Rolling back the binary requires logging in again because the versioned root key deliberately prevents both old and newly derived tokens from being accepted by the legacy authentication code. Password recovery remains available through the existing reset-password workflow.

Public exposure still requires operator-supplied HTTPS termination. The application warns about unsafe binding but does not attempt to manage certificates or reverse-proxy configuration.
