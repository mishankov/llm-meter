# Security Policy

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Report vulnerabilities privately through GitHub Security Advisories:

https://github.com/mishankov/llm-meter/security/advisories/new

Include the affected component, reproduction steps or proof of concept, and expected impact. Redact any live `DASHBOARD_TOKEN`, `llmm_...` proxy keys, or upstream provider credentials from logs and examples.

## Supported Versions

LLM Meter is currently in public alpha. Security fixes are provided for:

| Version | Supported |
| --- | --- |
| `main` | Yes |
| Latest alpha release | Yes |
| Older releases / snapshots | No |

## Security Boundaries

LLM Meter is designed as a self-hosted metering server with an optional local relay. Keep the following boundaries in mind when deploying and evaluating reports:

- **Dashboard authentication (`DASHBOARD_TOKEN`):** Unlocking the dashboard (`POST /api/login`) verifies `DASHBOARD_TOKEN` with constant-time SHA-256 comparison and issues a 24-hour HMAC-SHA256-signed `llm_meter_session` cookie (`HttpOnly`, `SameSite=Strict`, `Path=/api/`, `Secure` by default). Dashboard `/api/` routes enforce cross-origin request protection. `DASHBOARD_TOKEN` is never accepted as a proxy key, and `llm_meter_session` cookies are stripped before forwarding proxied requests upstream.
- **Client proxy keys (`X-LLM-Meter-Key`):** Client keys (`llmm_...`) are shown once on creation; the server persists only the SHA-256 digest and a short prefix. On proxy (`/v1`, `/antigravity`) and direct OpenTelemetry (`/otlp`) routes, the server validates `X-LLM-Meter-Key` and strips it before forwarding requests upstream. In `usage-only` mode, the relay strips any inbound `X-LLM-Meter-Key` and sends the configured client key only to the server's usage ingestion endpoint (`POST /api/usage/events`).
- **Loopback-only local relay:** `llm-meter-relay` requires a loopback `--listen` address (`127.0.0.1` or `[::1]`) and enforces an inbound request guard that rejects requests whose `Host` header does not match `--listen`, requests carrying `Origin` or `Sec-Fetch-Site` headers (mitigating cross-site browser requests to loopback), `CONNECT` requests, and path traversal segments. On Linux and macOS, `--key-file` must be a regular file accessible only to its owner (`0600`/`0400`). Published Docker ports must stay bound to `127.0.0.1`.
- **HTTPS requirements:** Production server deployments must sit behind an HTTPS reverse proxy or ingress with `COOKIE_SECURE=true` (`COOKIE_SECURE=false` is only for plain local `http://localhost:8080` testing). The relay requires an `https://` `--target` origin (and `https://` for any custom `--upstream`) and intentionally does not follow HTTP redirects on usage uploads.

For full deployment hardening guidance, see [docs/configuration.md](docs/configuration.md#6-security-networking-permissions-and-durable-queue-behavior).
