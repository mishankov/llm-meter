# LLM Meter Configuration Guide

Complete reference for deploying **LLM Meter** and **`llm-meter-relay`**, connecting OpenTelemetry and proxy clients, tuning pricing and security, and operating the stack.

- [1. Choose Your Setup](#1-choose-your-setup)
- [2. Set Up the LLM Meter Server](#2-set-up-the-llm-meter-server)
- [3. Client Keys, Authentication, and Endpoints](#3-client-keys-authentication-and-endpoints)
- [4. Run the Local Relay](#4-run-the-local-relay)
- [5. Connect Clients](#5-connect-clients)
- [6. Security, Networking, and Durable Queue Behavior](#6-security-networking-and-durable-queue-behavior)
- [7. Upgrades, Mode Changes, and Key Rotation](#7-upgrades-mode-changes-and-key-rotation)
- [8. Troubleshooting](#8-troubleshooting)

---

## 1. Choose Your Setup

All setups write to the same SQLite ledger and attribute usage to dashboard clients using client keys (`llmm_...`).

| Setup | Model traffic path | What the server sees | Per-client `X-LLM-Meter-Key` header needed? | Local relay queue (`/data`) needed? |
| --- | --- | --- | --- | --- |
| **Direct OpenTelemetry (`/otlp`)** | Client → Provider directly | Full exported telemetry (including any captured content); only extracted usage metadata is stored | Yes | No |
| **Relay (OpenTelemetry)** | Client → Provider directly | Filtered usage metadata only (`POST /api/usage/events`) | No (relay authenticates) | Yes (`usage-outbox.db`) |
| **Relay (`usage-only`)** | Client → Relay → Provider directly | Filtered usage metadata only (`POST /api/usage/events`) | No (relay authenticates) | Yes (`usage-outbox.db`) |
| **Relay (`remote-proxy`, default)** | Client → Relay → LLM Meter → Provider | Full proxied requests & upstream auth headers | No (relay injects it) | Only if OpenTelemetry is also enabled |
| **Direct proxy (`/v1`, `/antigravity`)** | Client → LLM Meter → Provider | Full proxied requests & upstream auth headers | Yes | No |

### When to pick each

- **Direct OpenTelemetry (`/otlp`):** Best for local deployments (`http://localhost:8080/otlp`) or simple setups where you don't want to run a separate relay process.
- **Local relay (OpenTelemetry):** Best when you run multiple coding CLIs on one machine and want a **single place to store and rotate your key** (macOS Keychain or one `proxy.key` file), **local prompt/content stripping** before data leaves your machine, and **offline SQLite queueing** across server outages.
- **Direct proxy (`/v1`, `/antigravity`):** Best for OpenAI-compatible SDKs/CLIs or Antigravity clients that can attach a custom `X-LLM-Meter-Key` header.
- **Local relay (`usage-only` or `remote-proxy`):** Best for proxy clients that only allow a base URL override (such as Antigravity ACP). Use `usage-only` to keep prompts and provider credentials off the LLM Meter server, or `remote-proxy` to route model traffic through the server.

---

## 2. Set Up the LLM Meter Server

One Go binary serves the dashboard UI, OTLP ingestion endpoints, reverse proxy routes, usage upload API, and `GET /healthz` (`204 No Content`) on a single port (default `:8080`).

### Published images

CI publishes multi-arch images at `ghcr.io/mishankov/llm-meter` for `linux/amd64` and `linux/arm64`:

| Tag | Meaning |
| --- | --- |
| `:latest` | Default branch (`main`) build |
| `:v0.1.0-alpha.1` | Release / prerelease tag |
| `:sha-<full-commit-sha>` | Pinned commit build |

The image runs as non-root user `llmmeter` (`10001:10001`), sets `DATA_DIR=/data`, exposes port `8080`, and embeds the built dashboard (`-tags embedui`). To build locally from a checkout, run `docker build -t llm-meter .`.

### Local testing with `docker run`

```sh
export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
echo "Dashboard token: $DASHBOARD_TOKEN"

docker run --rm -p 127.0.0.1:8080:8080 \
  -e DASHBOARD_TOKEN \
  -e COOKIE_SECURE=false \
  -v llm-meter-data:/data \
  ghcr.io/mishankov/llm-meter:latest
```

Open `http://localhost:8080` and unlock the dashboard with `DASHBOARD_TOKEN`.

### Docker Compose behind HTTPS

For remote or production access, put LLM Meter behind an HTTPS reverse proxy (Caddy, Nginx, Traefik, etc.) and keep `COOKIE_SECURE=true`. Save as `compose.yaml`:

```yaml
services:
  llm-meter:
    image: ghcr.io/mishankov/llm-meter:latest
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      DASHBOARD_TOKEN: ${DASHBOARD_TOKEN:?set DASHBOARD_TOKEN in your environment or .env}
      COOKIE_SECURE: "true"
      # Optional:
      # PRICING_FILE: /config/pricing.json
      # OPENAI_UPSTREAM: https://api.openai.com/v1
      # ANTIGRAVITY_UPSTREAM: https://daily-cloudcode-pa.googleapis.com
    volumes:
      - llm-meter-data:/data
      # - ./pricing.json:/config/pricing.json:ro

volumes:
  llm-meter-data:
```

Store a persistent `DASHBOARD_TOKEN` (for example in a `0600` `.env` file alongside `compose.yaml`) so browser sessions remain valid across restarts, then run:

```sh
docker compose up -d llm-meter
```

If your reverse proxy also runs in Docker, place both services on a shared network and proxy to `http://llm-meter:8080`.

### Native Go binary

Requires **Go 1.25+** and **Node.js 24**.

```sh
# Self-contained binary with embedded UI
npm ci --prefix web
npm run build --prefix web
CGO_ENABLED=0 go build -tags embedui -o llm-meter .

export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
LISTEN_ADDR=127.0.0.1:8080 COOKIE_SECURE=false DATA_DIR=./data ./llm-meter
```

For development without `-tags embedui`, pass `DASHBOARD_DIR=web/build` (the default) and run `go run .`.

### Server environment variables

| Variable | Default | Required | Notes |
| --- | --- | --- | --- |
| `DASHBOARD_TOKEN` | _(none)_ | Yes | 24–512 characters. Unlocks the dashboard (`POST /api/login`) and signs the `llm_meter_session` cookie. Never valid as an `X-LLM-Meter-Key`. |
| `COOKIE_SECURE` | `true` | No | `true` behind HTTPS; set `false` only for plain local HTTP. |
| `LISTEN_ADDR` | `:8080` | No | TCP listen address (e.g. `127.0.0.1:8080` or `:8080`). |
| `DATA_DIR` | `./data` (`/data` in Docker) | No | Directory for `llm-meter.db` (SQLite WAL mode). Created with `0700` if missing. |
| `PRICING_FILE` | _(unset)_ | No | Path to a JSON file of per-model rate overrides (USD per million tokens). |
| `OPENAI_UPSTREAM` | `https://api.openai.com/v1` | No | HTTP/HTTPS base URL for `/v1` proxy requests (no userinfo, query, or fragment). |
| `ANTIGRAVITY_UPSTREAM` | `https://daily-cloudcode-pa.googleapis.com` | No | HTTP/HTTPS base URL for `/antigravity` proxy requests (no userinfo, query, or fragment). |
| `DASHBOARD_DIR` | `web/build` | No | Static dashboard directory containing `index.html`. Ignored in Docker and `-tags embedui` builds. |

**Upstream provider detection:** When `OPENAI_UPSTREAM` points to another OpenAI-compatible host, LLM Meter infers the provider label for pricing lookup from known hosts: `api.openai.com` (`openai`), `openrouter.ai` (`openrouter`), `opencode.ai/inference/go` (`opencode-go`), `opencode.ai/inference` (`opencode`), `api.anthropic.com` (`anthropic`), `api.deepseek.com` (`deepseek`), `api.groq.com` (`groq`), `api.mistral.ai` (`mistral`), `api.x.ai` (`xai`), and `generativelanguage.googleapis.com` (`google`). Unknown hosts can be priced with `PRICING_FILE`.

### Pricing feeds and `PRICING_FILE`

Cost in USD is resolved in this order:

1. **Provider-reported amounts** (including `0`): exact reported cost, provider estimate, or provider list cost.
2. **`PRICING_FILE` overrides:** checked as `"<provider>/<model>"` first, then `"<model>"` (for Antigravity: `"google/<model>"`, `"antigravity/<model>"`, then `"<model>"`).
3. **Downloaded catalogs:** refreshed at startup and every 24 hours from OpenCode Go and Models.dev, cached in SQLite (`pricing_snapshots`) so feed outages do not lose rates.
4. **Built-in OpenAI rates:** standard text rates, long-context (>272,000 input tokens) multipliers, service-tier multipliers (`batch`, `flex`, `fast`, `priority`, `ultrafast`), and 2-hour (`0.7x`) / 8-hour (`0.6x`) background completion discounts.

`PRICING_FILE` maps model names (or `provider/model`) to non-negative rates in **USD per million tokens**. Include a rate for every token category reported by the model (use `0` for free categories); otherwise requests appear as **Unpriced**.

```json
{
  "my-model": {
    "input": 2,
    "cachedInput": 0.1,
    "output": 10,
    "cacheWrite": 2.5,
    "cacheWrite12h": 5
  },
  "openrouter/my-free-model": {
    "input": 0,
    "cachedInput": 0,
    "output": 0
  }
}
```

---

## 3. Client Keys, Authentication, and Endpoints

### Managing clients and keys

1. Open the dashboard, unlock with `DASHBOARD_TOKEN`, and go to **Clients**.
2. Create a client and copy its key (`llmm_...`). The full key is displayed **once**; the server stores only its SHA-256 digest and 13-character prefix.
3. A client can hold multiple active keys simultaneously for zero-downtime key rotation.
4. Open a client's details to **Rename** (updates labels everywhere, keys keep working) or **Archive** (removes from **Clients** and revokes all keys while keeping past data in **Usage**—drain any pending relay reports before archiving).

**Key scope:** One client key authenticates direct OTLP (`/otlp/v1/*`), proxy routes (`/v1`, `/antigravity`), and relay uploads (`POST /api/usage/events`). Create one client per machine for a combined view, or separate clients for per-tool attribution.

> **Background OpenAI responses (`background: true`):** Route both the initial `POST /v1/responses` and follow-up `GET /v1/responses/<id>` polls through the same client and measurement mode (`proxy` vs `relay`) so completion usage correlates to the original request.

### Authentication & endpoint matrix

| Integration | Direct to server | Via local relay | Client auth headers |
| --- | --- | --- | --- |
| **OpenTelemetry** | `<server>/otlp` (`/otlp/v1/{logs,traces,metrics}`) | `http(s)://127.0.0.1:4318/otlp` (`/otlp/v1/{logs,traces,metrics}`) | Direct: `X-LLM-Meter-Key` · Relay: none (relay uses `--key-file`) |
| **OpenAI-compatible** (`openai`) | `<server>/v1` | `http://127.0.0.1:8765/v1` | Provider auth (`Authorization`, `X-API-Key`, `Api-Key`, `X-Goog-Api-Key`) + `X-LLM-Meter-Key` for direct |
| **Signed-in Antigravity** (`antigravity`) | `<server>/antigravity` | `http://127.0.0.1:8765/antigravity` | Google `Authorization` + `X-LLM-Meter-Key` for direct |

- On direct proxy calls, the server validates and strips `X-LLM-Meter-Key` (and any `llm_meter_session` cookie) before forwarding upstream.
- Always use `127.0.0.1` (not `localhost`) when talking to the local relay with its default listeners; the relay enforces that HTTP `Host` matches `--listen` / `--telemetry-listen` (`localhost` returns `403 Forbidden`).

---

## 4. Run the Local Relay

On macOS, the [menu bar app](../desktop/README.md) manages the relay, Keychain key storage, local HTTPS certs, and client setup snippets for you. On Linux, Docker, or headless hosts, run `llm-meter-relay` directly.

### 4.1 Prepare the key file and state directory

Create an owner-only key file containing your client key (`llmm_...`) as its **only line**:

```sh
relay_dir="$HOME/.config/llm-meter/relay"
(umask 077; mkdir -p "$relay_dir/data"; touch "$relay_dir/proxy.key")
chmod 600 "$relay_dir/proxy.key"
```

### 4.2 Default single-service Docker Compose setup

The relay image is `ghcr.io/mishankov/llm-meter-relay` (`linux/amd64`, `linux/arm64`), using the same tag scheme as the server. To build locally, run `docker build -f Dockerfile.relay -t llm-meter-relay .`.

Save as `compose.yaml` and replace `https://proxy.example.com` with your server's HTTPS origin:

```yaml
services:
  relay:
    image: ghcr.io/mishankov/llm-meter-relay:latest
    user: "${RELAY_UID}:${RELAY_GID}"
    stop_grace_period: 15s
    ports:
      - "127.0.0.1:8765:8765"
      # Uncomment to enable the OpenTelemetry listener alongside the proxy:
      # - "127.0.0.1:4318:4318"
    volumes:
      - type: bind
        source: ${HOME}/.config/llm-meter/relay/proxy.key
        target: /run/secrets/proxy-key
        read_only: true
        bind:
          create_host_path: false
      - type: bind
        source: ${HOME}/.config/llm-meter/relay/data
        target: /data
        bind:
          create_host_path: false
    command:
      - --target=https://proxy.example.com
      - --key-file=/run/secrets/proxy-key
      - --bind=0.0.0.0:8765
      - --data-dir=/data
      # Optional:
      # - --mode=usage-only
      # - --telemetry-listen=127.0.0.1:4318
      # - --telemetry-bind=0.0.0.0:4318
      # - --proxy=false  # OpenTelemetry-only (also remove port 8765 and --bind)
    networks:
      - relay

networks:
  relay: {}
```

```sh
export RELAY_UID="$(id -u)" RELAY_GID="$(id -g)"
docker compose up -d relay
```

For HTTPS on the telemetry listener (e.g., local GitHub Copilot CLI), mount your PEM cert/key read-only and pass `--tls-cert` and `--tls-key`.

### 4.3 Native Go binary

```sh
go build -o llm-meter-relay ./cmd/llm-meter-relay
```

Common invocations:

```sh
# 1. OpenTelemetry-only relay (coding CLIs)
./llm-meter-relay \
  --proxy=false \
  --telemetry-listen=127.0.0.1:4318 \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/relay/proxy.key" \
  --data-dir "$HOME/.config/llm-meter/relay/data"

# 2. Usage-only model proxy + OpenTelemetry on the same relay
./llm-meter-relay \
  --mode usage-only \
  --telemetry-listen=127.0.0.1:4318 \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/relay/proxy.key" \
  --data-dir "$HOME/.config/llm-meter/relay/data"

# 3. Remote-proxy model relay (default mode)
./llm-meter-relay \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/relay/proxy.key"
```

### 4.4 Multiple instances, provider scoping, and `--upstream` overrides

Each relay instance shares one `--key-file` and `--data-dir` across its listeners. Run separate instances on distinct loopback ports (with separate `--data-dir` paths when queueing usage) if you want mixed modes, separate dashboard clients per tool, or a custom `--upstream` in `usage-only` mode:

| Goal | Flags | Result |
| --- | --- | --- |
| Both proxy routes via server | `--target https://proxy.example.com` | `/v1` and `/antigravity` → server |
| Both proxy routes direct | `--mode usage-only --target https://proxy.example.com` | Model traffic → default provider upstreams; usage → server |
| Single provider via server | `--provider antigravity --target https://proxy.example.com` | Only Antigravity enabled |
| Custom HTTPS upstream (`usage-only`) | `--mode usage-only --provider openai --target https://proxy.example.com --upstream https://openrouter.ai/api/v1` | `openai` → OpenRouter; usage → server |

In Docker Compose, if you map a non-default host port (e.g. `127.0.0.1:8766:8766`), pass matching `--listen=127.0.0.1:8766 --bind=0.0.0.0:8766`.

### 4.5 Relay CLI flags reference

Positional arguments are rejected; pass all options as flags.

| Flag | Default | Required | Description & validation |
| --- | --- | --- | --- |
| `--target` | _(none)_ | Yes | HTTPS LLM Meter server origin (no path, userinfo, query, or fragment). |
| `--key-file` | _(none)_ | Yes | Path to a regular, owner-only (`0600`/`0400`) file with a single printable ASCII key (max 4,096 bytes, no whitespace). |
| `--proxy` | `true` | No | Set `--proxy=false` with `--telemetry-listen` to run only the OTLP/HTTP receiver. |
| `--mode` | `remote-proxy` | No | `remote-proxy`, `usage-only`, or `telemetry` (standalone OTLP/HTTP on `--listen`/`--bind`). |
| `--provider` | `""` (both) | No | Restrict the proxy to `antigravity` or `openai`. |
| `--upstream` | `""` | No | Direct HTTPS provider base URL override (requires `--mode usage-only` and `--provider`; host must differ from `--target`). |
| `--listen` | `127.0.0.1:8765` | No | Client-facing loopback IP and port (`127.0.0.1:<port>` or `[::1]:<port>`), checked against incoming `Host`. |
| `--bind` | `""` | No | Container socket (`0.0.0.0:<nonzero-port>`) paired with a fixed loopback `--listen`. |
| `--telemetry-listen` | `""` (off) | No | Loopback IP and port for OTLP/HTTP (e.g. `127.0.0.1:4318`). Must differ from `--listen` when proxy is enabled. |
| `--telemetry-bind` | `""` | No | Container socket (`0.0.0.0:<nonzero-port>`) for `--telemetry-listen`. |
| `--data-dir` | `./data/relay` | No | Persistent `usage-outbox.db` directory for `usage-only` and OpenTelemetry. Use a separate directory per `(target, key)`. |
| `--tls-cert`, `--tls-key` | `""` | No | PEM certificate and private key enabling HTTPS on the OpenTelemetry listener. |

---

## 5. Connect Clients

### 5.1 Generic OpenAI-compatible clients

- **Direct:** Set the base URL to `https://proxy.example.com/v1` (or `http://localhost:8080/v1` locally) and attach `X-LLM-Meter-Key: <your-key>` alongside your normal `Authorization: Bearer <provider-api-key>`.
- **Via relay:** Set the base URL to `http://127.0.0.1:8765/v1` with your normal provider auth header (`Authorization`, `X-API-Key`, `Api-Key`, or `X-Goog-Api-Key`). Do not send `X-LLM-Meter-Key`.

### 5.2 Signed-in Antigravity

Keep Antigravity signed in to Google and keep both Google's token and your proxy key out of URLs, command arguments, and logs.

- **Direct (clients supporting custom headers):** Set the Cloud Code Assist base URL to `https://proxy.example.com/antigravity` and send `X-LLM-Meter-Key` alongside Google's `Authorization` header.
- **Via local relay (ACP / CLI endpoint override):** Run `llm-meter-relay` on the same machine and pass `--cloud_code_endpoint http://127.0.0.1:8765/antigravity` to `antigravity acp`. If your client only accepts a binary path, wrap Antigravity with an executable launcher:

```sh
#!/bin/sh
if [ "$1" = acp ]; then
  shift
  set -- acp --cloud_code_endpoint http://127.0.0.1:8765/antigravity "$@"
fi
exec "/absolute/path/to/antigravity" "$@"
```

Replace `/absolute/path/to/antigravity`, `chmod +x` the script, verify flag support with `antigravity acp --help`, and start a fresh session. The dashboard records requests under **Google** with the Antigravity endpoint. *(Antigravity CLI and ACP use this relay setup; Google's documented OpenTelemetry tracing applies to the Antigravity SDK.)* [Antigravity settings](https://antigravity.google/docs/settings?tab=cli) · [Antigravity SDK changelog](https://antigravity.google/docs/changelog)

### 5.3 OpenTelemetry

Both the **LLM Meter server** and the **local relay** expose the same OTLP/HTTP paths—`/otlp/v1/logs`, `/otlp/v1/traces`, and `/otlp/v1/metrics`—accepting JSON or protobuf (with optional gzip), extracting model usage and token/cache counts, discarding prompts and non-usage telemetry, and acknowledging metrics.

- **Direct to server:** Point the exporter at `http://localhost:8080/otlp` (local) or `https://proxy.example.com/otlp` (remote) and send `X-LLM-Meter-Key=<your-key>` via the CLI's OTLP header setting.
- **Via local relay:** Point the exporter at `http://127.0.0.1:4318/otlp` (or `https://127.0.0.1:4318/otlp` with TLS) with **no** OTLP auth header—the relay authenticates uploads using its key file or macOS Keychain. On macOS, enable **OpenTelemetry** in the [desktop app](../desktop/README.md) and press ⌘⇧E in **Client Setup** to copy the endpoint.

> Every example below disables prompt/content capture at the exporter as defense-in-depth. Exporters may batch reports, so give usage a few seconds to appear under **Usage**.

#### Claude Code

Reads `api_request` log events (including Claude Code's reported cost estimates). [Claude Code monitoring docs](https://code.claude.com/docs/en/monitoring-usage)

```sh
# Direct to server (for the local relay, use http://127.0.0.1:4318/otlp/v1/logs and unset OTEL_EXPORTER_OTLP_LOGS_HEADERS)
(
  unset OTEL_LOG_RAW_API_BODIES OTEL_EXPORTER_OTLP_HEADERS
  export CLAUDE_CODE_ENABLE_TELEMETRY=1
  export OTEL_LOGS_EXPORTER=otlp OTEL_METRICS_EXPORTER=none OTEL_TRACES_EXPORTER=none
  export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf
  export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:8080/otlp/v1/logs
  export OTEL_EXPORTER_OTLP_LOGS_HEADERS="X-LLM-Meter-Key=<your-key>"
  export OTEL_LOG_USER_PROMPTS=0 OTEL_LOG_ASSISTANT_RESPONSES=0
  export OTEL_LOG_TOOL_DETAILS=0 OTEL_LOG_TOOL_CONTENT=0
  claude
)
```

Use `claude --debug-file <path>` to inspect exporter errors if usage is missing.

#### Codex

Add to your user `~/.codex/config.toml` (merging into any existing `[otel]` table) and start a new session. Records each HTTP or WebSocket completion once and detects ChatGPT sign-in subscription traffic. [Codex observability docs](https://learn.chatgpt.com/docs/config-file/config-advanced#observability-and-telemetry)

```toml
# Direct to server (for the local relay, use http://127.0.0.1:4318/otlp/v1/logs and omit headers)
[otel]
exporter = { otlp-http = { endpoint = "http://localhost:8080/otlp/v1/logs", protocol = "json", headers = { "X-LLM-Meter-Key" = "<your-key>" } } }
log_user_prompt = false
log_agent_responses = false
log_guardian_assessments = false
log_sse_deltas = false
tool_result.max_bytes = 0
```

#### Gemini CLI

Reads API response logs (including tool prompt tokens as input and thinking tokens as output). Gemini CLI appends `/v1/logs` automatically. [Gemini CLI telemetry docs](https://geminicli.com/docs/cli/telemetry/)

```sh
# Direct to server (for the local relay, use http://127.0.0.1:4318/otlp and unset OTEL_EXPORTER_OTLP_HEADERS)
(
  unset GEMINI_TELEMETRY_OUTFILE GEMINI_TELEMETRY_OTLP_HEADERS OTEL_EXPORTER_OTLP_LOGS_HEADERS
  export GEMINI_TELEMETRY_ENABLED=true GEMINI_TELEMETRY_TARGET=local
  export GEMINI_TELEMETRY_OTLP_PROTOCOL=http
  export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://localhost:8080/otlp
  export OTEL_EXPORTER_OTLP_HEADERS="X-LLM-Meter-Key=<your-key>"
  export GEMINI_TELEMETRY_LOG_PROMPTS=false GEMINI_TELEMETRY_TRACES_ENABLED=false
  gemini
)
```

#### GitHub Copilot CLI

Reads individual model-call trace spans. [Copilot OpenTelemetry reference](https://docs.github.com/en/copilot/reference/enterprise-administrators/opentelemetry-reference)

- **Direct to an HTTPS LLM Meter server** (simplest—uses your server's TLS certificate):

```sh
COPILOT_OTEL_ENABLED=true \
COPILOT_OTEL_EXPORTER_TYPE=otlp-http \
OTEL_EXPORTER_OTLP_ENDPOINT=https://proxy.example.com/otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json \
OTEL_EXPORTER_OTLP_HEADERS="X-LLM-Meter-Key=<your-key>" \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false \
copilot
```

- **Via local HTTPS relay:** Create a loopback certificate with `mkcert` ([mkcert usage](https://github.com/FiloSottile/mkcert#usage)), configure `--tls-cert` and `--tls-key` on the relay (or **Settings → Local HTTPS** in the macOS app), and trust the local CA:

```sh
relay_tls_dir="$HOME/.config/llm-meter/relay/local-tls"
(
  umask 077
  mkdir -p "$relay_tls_dir"
  CAROOT="$relay_tls_dir" mkcert \
    -cert-file "$relay_tls_dir/loopback.pem" \
    -key-file "$relay_tls_dir/loopback-key.pem" 127.0.0.1
)

# Start the relay with --tls-cert "$relay_tls_dir/loopback.pem" --tls-key "$relay_tls_dir/loopback-key.pem", then run:
COPILOT_OTEL_ENABLED=true \
COPILOT_OTEL_EXPORTER_TYPE=otlp-http \
OTEL_EXPORTER_OTLP_ENDPOINT=https://127.0.0.1:4318/otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json \
OTEL_EXPORTER_OTLP_CERTIFICATE="$relay_tls_dir/rootCA.pem" \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false \
copilot
```

#### OpenCode

Meters model usage from `SessionStep.attempt` trace spans. Launch with `--standalone` so the environment applies to the session server. [OpenCode CLI reference](https://opencode.ai/docs/cli/#background-service) · [OpenCode OTLP implementation](https://github.com/anomalyco/opencode/blob/v2.3.2/packages/util/src/observability/otlp.ts)

```sh
# Direct to server (for the local relay, use http://127.0.0.1:4318/otlp and unset OTEL_EXPORTER_OTLP_HEADERS)
(
  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:8080/otlp \
  OTEL_EXPORTER_OTLP_HEADERS="X-LLM-Meter-Key=<your-key>" \
  opencode --standalone
)
```

---

## 6. Security, Networking, and Durable Queue Behavior

### Relay networking & request guard

- **Bind published Docker ports to loopback only:** Always publish `"127.0.0.1:8765:8765"` (and `"127.0.0.1:4318:4318"`), never `"8765:8765"`. The relay injects your client key on behalf of local callers.
- **Docker Engine 28+ & private network:** Keep the relay on a dedicated Compose bridge network and attach only trusted containers. [Docker bridge networking](https://docs.docker.com/engine/network/drivers/bridge/) · [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/#publishing-ports)
- **Container `--listen` vs `--bind`:** `--bind=0.0.0.0:<port>` listens on the container socket for Docker port forwarding, while `--listen=127.0.0.1:<port>` is the host-facing loopback address checked against each request's `Host` header.
- **Request guard:** The relay rejects mismatched `Host` headers (`403`), browser `Origin` or `Sec-Fetch-Site` headers (`403`), `CONNECT` / unknown paths / path traversal (`404`), and proxy requests missing upstream provider auth (`401`).

### Key file permissions & container UID/GID

`--key-file` must be a regular file, owner-only (`mode & 0077 == 0`, i.e. `0600` or `0400`) on Linux/macOS, containing one printable ASCII key (max 4,096 bytes, trailing newline allowed).

- **Recommended (Compose default):** Run with `user: "${RELAY_UID}:${RELAY_GID}"` matching the host owner of `proxy.key` and `data`, with `bind: { create_host_path: false }`.
- **Alternative (built-in image user):** Omit `user:` to run as `10001:10001` and `chown 10001:10001` on `proxy.key` (`0600`) and `data` (`0700`).

### Persistent `/data` and `(server, key)` binding

- **Server (`DATA_DIR`):** Always persistent; stores `llm-meter.db` (clients, hashed keys, ledger, cached pricing).
- **Relay (`--data-dir`):** Required for `usage-only` and OpenTelemetry modes; stores `usage-outbox.db`. On first open, `usage-outbox.db` binds itself to a SHA-256 fingerprint of `(upload URL, proxy key)` and refuses to start against a different target or key so queued events are never attributed to the wrong client or server.

### Durable queue, retries, and graceful shutdown

- **Capacity & limits:** Up to **10,000** queued events (`16 KiB` max per event). When full, `usage-only` logs `Could not queue a usage report` and drops the report (model traffic continues); OpenTelemetry returns `503` so the exporter can retry.
- **Ordered delivery & deduplication:** A background worker uploads events in FIFO order to `POST https://<target>/api/usage/events` (`10s` timeout, no redirects followed). The server returns `204` and deduplicates atomically by `(client_id, event_id)`.
- **Retry vs discard:**
  - **Retryable** (network error, timeout, `401`, `404`, `405`, `429`, `5xx`, redirects): stays queued with exponential backoff from `1s` to `60s`, logging `Usage event remains queued`.
  - **Permanent** (`400`, `413`, `422`): event is discarded so it cannot block the queue, logging `Discarded usage event rejected by the server`.
- **Graceful shutdown:** On `SIGINT`/`SIGTERM`, the relay stops uploading, closes open WebSockets, and gives in-flight handlers up to `10s` to persist usage before exiting (keep `stop_grace_period: 15s` in Docker Compose).

---

## 7. Upgrades, Mode Changes, and Key Rotation

### Upgrading the server

Keep `/data` and `DASHBOARD_TOKEN`, pull the new image, and confirm `/healthz` returns `204`:

```sh
docker compose pull llm-meter && docker compose up -d llm-meter
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz
```

### Upgrading the relay

```sh
export RELAY_UID="$(id -u)" RELAY_GID="$(id -g)"
docker compose pull relay && docker compose up -d relay
```

For native binaries, rebuild with `go build -o llm-meter-relay ./cmd/llm-meter-relay` and restart using the same `--data-dir`.

### Checking pending relay reports

Pause new client requests and check the outbox count:

```sh
sqlite3 -readonly "$HOME/.config/llm-meter/relay/data/usage-outbox.db" 'SELECT COUNT(*) FROM events;'
```

Wait for `0` before switching from `usage-only` to `remote-proxy`, rotating a key, or archiving a client.

### Switching relay modes

- **`remote-proxy` → `usage-only`:** Ensure `--data-dir` is persistent, add `--mode=usage-only`, and restart.
- **`usage-only` → `remote-proxy`:** Wait for pending reports to reach `0`, remove `--mode=usage-only`, and restart.

### Rotating client keys or moving servers

1. Create a **new key** for the client in **Clients** (keep the old key active).
2. If using the relay with a queue, wait for pending reports to reach `0`, then stop the relay.
3. Update `proxy.key` (keeping `0600`), update `--target` if moving servers, and point `--data-dir` at a **fresh empty directory** (each outbox is bound to its original `(server, key)`).
4. Start the relay (or update direct client headers), send a test request, verify it appears in **Usage**, and revoke the old key in **Clients**.

---

## 8. Troubleshooting

### Server startup, dashboard, and direct requests

| Symptom / Error | Likely cause | Fix |
| --- | --- | --- |
| `DASHBOARD_TOKEN must contain 24 to 512 characters` | Missing or too-short token | Generate one with `openssl rand -hex 32`. |
| Dashboard immediately asks to unlock again | Plain `http://` while `COOKIE_SECURE=true` | Set `COOKIE_SECURE=false` for local HTTP, or serve over HTTPS. |
| `dashboard build unavailable` (native run) | Built without `-tags embedui` and `web/build/index.html` missing | Run `npm ci --prefix web && npm run build --prefix web`, set `DASHBOARD_DIR`, or build with `-tags embedui`. |
| `401 A valid X-LLM-Meter-Key is required` on `/otlp/*`, `/v1/*`, or `/antigravity/*` | Sent `DASHBOARD_TOKEN`, missing/duplicate `X-LLM-Meter-Key`, or revoked key | Create a client key (`llmm_...`) in **Clients** and send exactly one `X-LLM-Meter-Key` header. |
| `404 Not Found` on `/otlp/...` | Wrong OTLP signal path (e.g. trailing slash or missing `/v1/logs`) | Use `/otlp/v1/logs`, `/otlp/v1/traces`, or `/otlp/v1/metrics` (base endpoint `<server>/otlp`). |

### Relay startup and client errors

| Symptom / Error | Likely cause | Fix |
| --- | --- | --- |
| `--target must be an HTTPS LLM Meter origin...` | HTTP URL, path, credentials, query, or fragment in `--target` | Use only the HTTPS origin, e.g. `https://proxy.example.com`. |
| `--upstream requires --mode usage-only` / `--provider` | Used `--upstream` without both flags | Pass `--mode usage-only --provider <openai\|antigravity>`. |
| `the provider and LLM Meter must use different hosts` | `--upstream` shares a hostname with `--target` | Point `--target` at LLM Meter and `--upstream` at the provider. |
| `cannot open the proxy key file` / `accessible only to its owner` / `single key` | Missing file, loose permissions, UID mismatch, or extra lines | Make `proxy.key` a single-line regular file, `chmod 600`, and match `RELAY_UID`/`RELAY_GID` (or `10001:10001`). |
| `use a separate usage queue directory for a different server or key` | Reused `--data-dir` after changing `--target` or key | Drain old reports if needed, then point `--data-dir` at a fresh empty directory. |
| `403 Forbidden` from relay | `Host` doesn't match `--listen` (e.g. `localhost` instead of `127.0.0.1`), or browser `Origin`/`Sec-Fetch-Site` header | Use `127.0.0.1:<port>` matching `--listen` and don't call from browser contexts. |
| `401 Provider/Google authorization is required` | Client omitted upstream credentials | Confirm provider API key or Google sign-in is active. |
| `502 LLM Meter unavailable` / `502 Upstream unavailable` | Relay cannot reach server or provider over HTTPS | Check DNS, TLS trust, firewall/VPN, and `GET /healthz`. |

### Usage uploads, pricing, and correlation

| Symptom / Log message | Likely cause | Fix |
| --- | --- | --- |
| `Usage event remains queued` (`401`, `404`, `405`, `3xx`) | Revoked key, reverse proxy not routing `POST /api/usage/events`, or redirecting | Verify active key and canonical HTTPS origin (redirects are not followed). |
| `Could not queue a usage report` | Queue hit 10,000-event cap or `--data-dir` unwritable/full | Restore server connectivity to drain the queue; check `--data-dir` ownership and disk space. |
| `Discarded usage event rejected by the server` (`400`/`413`/`422`) | Version mismatch or host clock >10 minutes ahead of server | Sync clocks (NTP) and keep server and relay versions aligned. |
| Requests show **Unpriced** | Provider omitted cost and model/category lacks a rate (or Models.dev flags separate audio rates) | Check pricing feed logs or add complete rates in `PRICING_FILE`. |
| Background response tokens missing on original request | `POST /v1/responses` and `GET /v1/responses/<id>` used different clients or mixed `proxy`/`relay` modes | Keep creation and polling on the same client and mode. |
