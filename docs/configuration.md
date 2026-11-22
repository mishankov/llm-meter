# LLM Meter Configuration Guide

This guide covers every supported way to deploy **LLM Meter** and **`llm-meter-relay`**, configure server and relay options, connect clients, manage upgrades and key rotation, and troubleshoot common issues.

- [1. Choose Your Setup](#1-choose-your-setup)
- [2. Set Up the LLM Meter Server](#2-set-up-the-llm-meter-server)
- [3. Proxy Keys, Authentication, and Client Base Paths](#3-proxy-keys-authentication-and-client-base-paths)
- [4. Run the Local Relay](#4-run-the-local-relay)
- [5. Connect Clients](#5-connect-clients)
- [6. Security, Networking, Permissions, and Durable Queue Behavior](#6-security-networking-permissions-and-durable-queue-behavior)
- [7. Lifecycle, Upgrades, and Key Rotation](#7-lifecycle-upgrades-and-key-rotation)
- [8. Troubleshooting](#8-troubleshooting)

---

## 1. Choose Your Setup

LLM Meter supports three topologies. All three feed the same dashboard ledger and use dashboard client proxy keys (`llmp_...`) for attribution.

| Setup | Model traffic path | What the LLM Meter server sees | Client custom header (`X-LLM-Proxy-Key`) needed? | Local relay `/data` state needed? | Server version requirement | Request `measurementSource` |
| --- | --- | --- | --- | --- | --- | --- |
| **Direct client-to-server** | Client → LLM Meter → Provider | Full proxied requests, responses, and upstream auth headers | Yes | No | Server supports the selected provider route | `proxy` |
| **Relay (`remote-proxy`, default)** | Client → Local Relay → LLM Meter → Provider | Full proxied requests, responses, and upstream auth headers | Relay supplies it | Optional for switching to `usage-only` | Server supports the selected provider route | `proxy` |
| **Relay (`usage-only`)** | Client → Local Relay → Provider. Relay reports to LLM Meter. | Usage metadata only (model, canonical route, status, tokens, costs, quotas) | Relay supplies it for uploads | Yes (`usage-outbox.db` SQLite queue) | Server supports `POST /api/usage/events` | `relay` |

### When to pick each setup

- **Choose direct client-to-server** when your client can send custom headers (OpenAI SDKs, OpenCode, signed-in Codex) and you want central routing and server-side `*_UPSTREAM` overrides without running a local relay process.
- **Choose relay in `remote-proxy` mode** when a client only supports an endpoint override without custom headers (for example, Antigravity ACP), or when you want all tools on a workstation to point at `http://127.0.0.1:8765` with one local key file while still routing traffic through your LLM Meter server.
- **Choose relay in `usage-only` mode** when you want prompts, responses, and provider credentials to stay between your machine and the model provider while still tracking spend, tokens, and Codex quotas in your central LLM Meter dashboard.

Upgrade the LLM Meter server to a version that supports `POST /api/usage/events` before enabling `--mode usage-only`. An older server typically returns `404` or `405`, leaving events queued locally.

In `usage-only` mode, the server authenticates uploads with the dashboard client's proxy key and trusts that client to report its own usage. Prompt and response content and provider credentials travel between the client, relay, and provider. Uploads contain canonical route templates such as `/v1/responses/:id`, hashed response IDs, token counts, request outcomes, reported costs, and quota readings.

---

## 2. Set Up the LLM Meter Server

Go serves both the reverse proxy endpoints and the SvelteKit dashboard from a single binary on one port (default `:8080`). A lightweight health check is available at `GET /healthz` (returns `204 No Content`).

### Published images

CI builds `ghcr.io/mishankov/llm-meter` for `linux/amd64` and `linux/arm64`. Sign in to GHCR if your account needs access to the package, or build the image locally.

| Tag pattern | Meaning |
| --- | --- |
| `:latest` | Default branch build |
| `:branch-<branch-name>` | Branch build (`/` replaced by `-`) |
| `:sha-<full-commit-sha>` | Pinned commit build |
| `:v1.2.3` | Release semver tag |

To build an image from your checkout, run the following command.

```sh
docker build -t llm-meter .
```

The image runs as non-root user `llmproxy` (UID/GID `10001`), sets `DATA_DIR=/data`, exposes port `8080`, declares `VOLUME ["/data"]`, and embeds the built dashboard UI (`-tags embedui`).

### Docker with `docker run`

For local testing, start the server on loopback with HTTP.

```sh
export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
docker run --rm -p 127.0.0.1:8080:8080 \
  -e DASHBOARD_TOKEN \
  -e COOKIE_SECURE=false \
  -v llm-proxy-data:/data \
  ghcr.io/mishankov/llm-meter:latest
```

Open `http://localhost:8080` and unlock the dashboard with `DASHBOARD_TOKEN`.

### Docker Compose behind HTTPS

Serve the container behind an HTTPS ingress and keep `COOKIE_SECURE=true`. The example publishes port `8080` on the Docker host's loopback interface for an ingress running on that host. For a containerized ingress, put the two services on a shared network and point the ingress at `http://llm-meter:8080`. Save this file as `compose.yaml`.

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
      # Optional: uncomment to override upstream base URLs or custom model rates.
      # PRICING_FILE: /config/pricing.json
      # OPENAI_UPSTREAM: https://api.openai.com/v1
      # CODEX_UPSTREAM: https://chatgpt.com/backend-api/codex
      # OPENCODE_GO_UPSTREAM: https://opencode.ai/inference/go
      # ANTIGRAVITY_UPSTREAM: https://daily-cloudcode-pa.googleapis.com
    volumes:
      - llm-proxy-data:/data
      # - ./pricing.json:/config/pricing.json:ro

volumes:
  llm-proxy-data:
```

Set a persistent `DASHBOARD_TOKEN`, for example in a `0600` `.env` file beside `compose.yaml`, then start the server.

```sh
docker compose up -d llm-meter
```

Keep `DASHBOARD_TOKEN` stable across restarts so existing 24-hour browser sessions stay valid and your login secret does not change.

### Native Go binary

Use Go `1.25+` and Node.js `24`.

1. To build a self-contained binary, embed the dashboard UI.

   ```sh
   npm ci --prefix web
   npm run build --prefix web
   CGO_ENABLED=0 go build -tags embedui -o llm-meter .

   export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
   LISTEN_ADDR=127.0.0.1:8080 COOKIE_SECURE=false DATA_DIR=./data ./llm-meter
   ```

2. For development or an external dashboard build, run with `DASHBOARD_DIR`.

   ```sh
   npm ci --prefix web
   npm run build --prefix web

   export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
   LISTEN_ADDR=127.0.0.1:8080 COOKIE_SECURE=false DASHBOARD_DIR=web/build go run .
   ```

### Server environment variables

| Variable | Default | Required | Notes |
| --- | --- | --- | --- |
| `DASHBOARD_TOKEN` | _(none)_ | Yes | 24–512 characters. Unlocks the dashboard UI/API (`POST /api/login`) and signs the `llm_proxy_session` cookie. Never valid as a proxy client key (`X-LLM-Proxy-Key`). |
| `COOKIE_SECURE` | `true` | No | `true` or `false`. Keep `true` behind HTTPS. Set `false` only for plain local HTTP (`http://localhost:8080`). |
| `LISTEN_ADDR` | `:8080` | No | TCP listen address (for example `127.0.0.1:8080` or `:8080`). |
| `DATA_DIR` | `./data` (`/data` in Docker) | No | Stores `llm-proxy.db` (SQLite in WAL mode). The server creates a missing directory with mode `0700`. |
| `PRICING_FILE` | _(unset)_ | No | Path to a JSON file of per-model rate overrides in USD per million tokens. |
| `OPENAI_UPSTREAM` | `https://api.openai.com/v1` | No | HTTP/HTTPS base URL for `/v1` requests. Must omit userinfo, query, and fragment. |
| `CODEX_UPSTREAM` | `https://chatgpt.com/backend-api/codex` | No | HTTP/HTTPS base URL for `/codex` requests. Must omit userinfo, query, and fragment. |
| `OPENCODE_GO_UPSTREAM` | `https://opencode.ai/inference/go` | No | HTTP/HTTPS base URL for `/opencode-go` requests. Must omit userinfo, query, and fragment. |
| `ANTIGRAVITY_UPSTREAM` | `https://daily-cloudcode-pa.googleapis.com` | No | HTTP/HTTPS base URL for `/antigravity` requests. Must omit userinfo, query, and fragment. |
| `DASHBOARD_DIR` | `web/build` | No | Path to static dashboard assets containing `index.html`. Used **only** when built without `-tags embedui`; ignored in Docker and `-tags embedui` binaries. |

#### Upstream override behavior

- **`OPENAI_UPSTREAM` provider detection:** When you point `OPENAI_UPSTREAM` at another OpenAI-compatible host, LLM Meter infers the provider label for pricing lookup from known hostnames: `api.openai.com` (`openai`), `openrouter.ai` (`openrouter`), `opencode.ai/inference/go` (`opencode-go`), `opencode.ai/inference` (`opencode`), `api.anthropic.com` (`anthropic`), `api.deepseek.com` (`deepseek`), `api.groq.com` (`groq`), `api.mistral.ai` (`mistral`), `api.x.ai` (`xai`), and `generativelanguage.googleapis.com` (`google`). Unknown hosts are recorded without a provider label and can be priced via `PRICING_FILE`.
- **`OPENCODE_GO_UPSTREAM` protocol routing:** LLM Meter automatically maps `/opencode-go` subpaths onto OpenCode Go's backend prefixes: `/models` and `/usage` → `/v1`, `/messages*` → `/anthropic/v1`, `/models/<id>:<action>` → `/google/v1beta`, and all other paths → `/openai/v1`.
- **In `usage-only` mode:** Server `*_UPSTREAM` variables do not affect traffic sent directly from a `usage-only` relay to providers. To override a provider base URL in `usage-only` mode, use the relay's `--upstream` flag instead (see [Section 4.6](#46-scoping-legacy-targets-and-optional-https---upstream-overrides)).

### Pricing feeds and `PRICING_FILE`

The server checks these sources in order when recording a request's cost in USD.

1. **Provider-reported amounts** (including `0`): exact reported cost, provider estimate, or provider list cost. For OpenCode Go, list value and allowance use are recorded alongside any reported charge.
2. **`PRICING_FILE` overrides**: checked first as `"<provider>/<model>"` (for example `"openai/my-model"`), then as `"<model>"`.
3. **Downloaded catalogs**: refreshed at startup and every 24 hours from OpenCode Go (`https://opencode.ai/inference/go/v1/models`) and Models.dev (`https://models.dev/api.json?type=all`), and cached in SQLite (`pricing_snapshots`) so temporary feed outages do not lose rates.
4. **Built-in OpenAI rates**: standard text rates, long-context (>272,000 input tokens) multipliers, service-tier multipliers (`batch`, `flex`, `fast`, `priority`, `ultrafast`), and 2-hour (`0.7x`) / 8-hour (`0.6x`) background completion discounts on eligible OpenAI models when not using a `PRICING_FILE` override.

Catalog refreshes apply to new requests. Background completions and usage replays can fill in or refine an existing request's usage and pricing.

#### `PRICING_FILE` format

Provide a JSON object mapping model names (or `provider/model`) to non-negative rates in **USD per million tokens**.

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

Include a rate for every token category a request reports. Use `0` when a category is free. When a request has no provider-reported cost or applicable override, automatic pricing can leave it **Unpriced** if a rate is missing or the catalog lists separate audio rates. After upgrading from an older catalog snapshot, allow one successful Models.dev refresh to load the audio-rate metadata.

---

## 3. Proxy Keys, Authentication, and Client Base Paths

### Creating and scoping proxy keys

1. Open the dashboard, unlock it with `DASHBOARD_TOKEN`, and go to **Clients**.
2. Create a client and copy its proxy key (`llmp_...`). The full key is displayed **only once**; the server stores only its SHA-256 digest and 13-character prefix.
3. Keep multiple active keys for the same client during rotation. Revoke the old key once the replacement is in use.

**Key scope:**

- One proxy key authenticates across **all four proxy routes** (`/v1`, `/codex`, `/opencode-go`, `/antigravity`) **and** the usage ingestion endpoint (`POST /api/usage/events`).
- All requests using keys belonging to the same dashboard client are grouped under that client, while retaining their individual provider labels (`openai`, `codex`, `opencode-go`, `antigravity`, etc.).
- Create **one client** per workstation/relay if you want one combined client view, or **separate clients** (with separate keys, and separate relay instances if using the relay) if you want per-tool or per-provider attribution in **Clients** and **Usage**.
- **Background responses (`background: true`):** Keep both the initial `POST /v1/responses` and follow-up `GET /v1/responses/<id>` polls on the **same dashboard client and measurement mode** (`proxy` vs `relay`) so the server can correlate completion usage onto the originating request.

### Normal provider auth vs. `X-LLM-Proxy-Key`

| Connection path | What the client sends | What LLM Meter / Relay does with headers |
| --- | --- | --- |
| **Direct to server** | Client's normal upstream auth (`Authorization`, API key, etc.) **plus** one `X-LLM-Proxy-Key` header | Server validates `X-LLM-Proxy-Key`, strips `X-LLM-Proxy-Key` (and any `llm_proxy_session` cookie), and forwards the client's upstream auth to the provider. |
| **Via relay (`remote-proxy`)** | Client's normal upstream auth only (no `X-LLM-Proxy-Key` needed) | Relay requires provider auth before forwarding, sets/replaces `X-LLM-Proxy-Key` from `--key-file`, and forwards over HTTPS to LLM Meter. |
| **Via relay (`usage-only`)** | Client's normal upstream auth only (no `X-LLM-Proxy-Key` needed) | Relay requires provider auth, strips any inbound `X-LLM-Proxy-Key`, forwards directly over HTTPS to the provider, and sends `--key-file` only in `X-LLM-Proxy-Key` to `POST /api/usage/events`. |

### All four client base paths

| Provider | Direct to LLM Meter server | Via local relay (`127.0.0.1:8765`) | Server path (`remote-proxy`) | Provider auth accepted by relay | Default upstream |
| --- | --- | --- | --- | --- | --- |
| **OpenAI-compatible** (`openai`) | `https://proxy.example.com/v1` | `http://127.0.0.1:8765/v1` | `/v1` | `Authorization`, `X-API-Key`, `Api-Key`, or `X-Goog-Api-Key` | `https://api.openai.com/v1` |
| **Signed-in Codex** (`codex`) | `https://proxy.example.com/codex` | `http://127.0.0.1:8765/codex` | `/codex` | `Authorization` (ChatGPT sign-in) or API key headers above | `https://chatgpt.com/backend-api/codex` |
| **OpenCode Go** (`opencode-go`) | `https://proxy.example.com/opencode-go` | `http://127.0.0.1:8765/opencode-go` | `/opencode-go` | `Authorization`, `X-API-Key`, `Api-Key`, or `X-Goog-Api-Key` | `https://opencode.ai/inference/go` |
| **Signed-in Antigravity** (`antigravity`) | `https://proxy.example.com/antigravity` | `http://127.0.0.1:8765` | `/antigravity` | `Authorization` (Google sign-in only) | `https://daily-cloudcode-pa.googleapis.com` |

> **Note on Antigravity's local relay URL:** Point Antigravity at the relay root `http://127.0.0.1:8765` (without `/antigravity`). The relay matches Cloud Code RPC paths (`/v1internal:<Method>`) and rewrites them to `/antigravity/v1internal:<Method>` in `remote-proxy` mode or sends `/v1internal:<Method>` directly to the Antigravity upstream in `usage-only` mode.
>
> Use `127.0.0.1` in client URLs with the default listener. The relay checks the request's `Host` against `--listen`. Using `localhost:8765` with the default listener returns `403 Forbidden`.

---

## 4. Run the Local Relay

One relay instance serves **all four providers** on the same port (`http://127.0.0.1:8765` by default). Set `--target` to your LLM Meter server's HTTPS origin (`https://proxy.example.com`) and point `--key-file` at a private file containing one dashboard client key.

### 4.1 Prepare the key file and state directory

Create a private directory and key file with owner-only permissions.

```sh
relay_dir="$HOME/.config/llm-meter/relay"
(umask 077; mkdir -p "$relay_dir/data"; touch "$relay_dir/proxy.key")
chmod 600 "$relay_dir/proxy.key"
```

Write your dashboard client proxy key (`llmp_...`) as the **only line** in `$HOME/.config/llm-meter/relay/proxy.key` (no extra lines, spaces, or comments).

### 4.2 Default single-service Docker Compose setup

The relay image is `ghcr.io/mishankov/llm-meter-relay` for `linux/amd64` and `linux/arm64`, using the same tag scheme as the server. Use an image tag that includes the features you want. Authenticate to GHCR if the package is private, or build an image from your checkout.

```sh
docker build -f Dockerfile.relay -t llm-meter-relay .
```

Save this configuration as `compose.yaml` and replace `https://proxy.example.com` with your LLM Meter server's HTTPS origin. If you built the image locally, set `image: llm-meter-relay`.

```yaml
services:
  relay:
    image: ghcr.io/mishankov/llm-meter-relay:latest
    user: "${RELAY_UID}:${RELAY_GID}"
    stop_grace_period: 15s
    ports:
      - "127.0.0.1:8765:8765"
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
    networks:
      - relay

networks:
  relay: {}
```

Start the relay.

```sh
export RELAY_UID="$(id -u)" RELAY_GID="$(id -g)"
docker compose up -d relay
```

- **To switch this service to `usage-only` mode**, add `- --mode=usage-only` under `command` (after upgrading your LLM Meter server to a version that supports `POST /api/usage/events`) and re-run `docker compose up -d relay`.
- **Keep `/data` mounted** in `usage-only` mode so queued usage reports survive container recreation.

### 4.3 Equivalent native Go binary commands

Build the relay binary.

```sh
go build -o llm-meter-relay ./cmd/llm-proxy-relay
```

- Run the relay for all four providers on `127.0.0.1:8765` in the default `remote-proxy` mode.

  ```sh
  ./llm-meter-relay \
    --target https://proxy.example.com \
    --key-file "$HOME/.config/llm-meter/relay/proxy.key"
  ```

- Run the relay on the same port in `usage-only` mode.

  ```sh
  ./llm-meter-relay \
    --mode usage-only \
    --target https://proxy.example.com \
    --key-file "$HOME/.config/llm-meter/relay/proxy.key" \
    --data-dir "$HOME/.config/llm-meter/relay/data"
  ```

### 4.4 Per-instance mode choice and running multiple instances

`--mode`, `--key-file`, and `--data-dir` apply to an entire relay instance. Most users only need the single default instance above. Run a second relay instance (on a separate loopback port, and with its own `--data-dir` if using `usage-only`) when you want:

- **Mixed modes** on the same machine — for example, `usage-only` for signed-in Codex/Antigravity on `127.0.0.1:8765` and `remote-proxy` for OpenAI-compatible tools on `127.0.0.1:8766`.
- **Separate dashboard clients** — different `--key-file` files so each tool or provider appears under its own client name in the dashboard.
- **Per-provider `--upstream` override** in `usage-only` mode — since `--upstream` scopes an instance to one provider.

These commands run two isolated native instances. Run each in its own terminal.

```sh
# Instance 1: usage-only for Codex + Antigravity on 127.0.0.1:8765
./llm-meter-relay \
  --mode usage-only \
  --listen 127.0.0.1:8765 \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/codex-ag.key" \
  --data-dir "$HOME/.config/llm-meter/codex-ag-data"

# Instance 2: remote-proxy for OpenAI-compatible tools on 127.0.0.1:8766
./llm-meter-relay \
  --provider openai \
  --listen 127.0.0.1:8766 \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/openai.key"
```

In Docker Compose, if you expose a second instance on host port `8766`, match `--listen` to the host-facing loopback address and port (for example `ports: ["127.0.0.1:8766:8766"]` with `--listen=127.0.0.1:8766 --bind=0.0.0.0:8766`).

### 4.5 Relay CLI flags reference

Positional arguments are rejected; pass all options as flags.

| Flag | Default | Required | Description & validation |
| --- | --- | --- | --- |
| `--target` | _(none)_ | Yes | HTTPS LLM Meter URL (`https://...`), without userinfo, query, or fragment. Must be an origin only in `usage-only` mode; in `remote-proxy` mode also accepts a legacy provider route suffix. |
| `--key-file` | _(none)_ | Yes | Path to a private regular file containing a single proxy key (max 4,096 bytes, one line, printable ASCII, no whitespace). Must be owner-only (`0600`/`0400`) on Linux/macOS. |
| `--mode` | `remote-proxy` | No | `remote-proxy` or `usage-only`. |
| `--provider` | `""` (all 4) | No | Restrict the instance to one route: `antigravity`, `openai`, `codex`, or `opencode-go`. |
| `--upstream` | `""` | No | Direct HTTPS provider base URL override (no userinfo, query, or fragment). Only valid with `--mode usage-only`. Hostname must differ from `--target`. If `--provider` is omitted, scopes the instance to `antigravity`. |
| `--listen` | `127.0.0.1:8765` | No | Client-facing loopback IP and port (`127.0.0.1:<port>` or `[::1]:<port>`). Enforced against the incoming HTTP `Host` header. |
| `--bind` | `""` | No | Container socket address. When set, must be `0.0.0.0:<nonzero-port>` paired with a fixed non-zero loopback `--listen`. |
| `--data-dir` | `./data/relay` | No | Persistent `usage-outbox.db` directory for `usage-only`. Give each relay instance its own queue directory. |

### 4.6 Scoping, legacy targets, and optional HTTPS `--upstream` overrides

| Goal | Flags | Resulting routes & destination |
| --- | --- | --- |
| All 4 providers via LLM Meter (default) | `--target https://proxy.example.com` | All 4 routes → `https://proxy.example.com/{antigravity,v1,codex,opencode-go}` |
| All 4 providers, direct to standard upstreams | `--mode usage-only --target https://proxy.example.com` | All 4 routes → each provider's default upstream; usage → `https://proxy.example.com/api/usage/events` |
| Single provider via LLM Meter | `--provider codex --target https://proxy.example.com` | `codex` only → `https://proxy.example.com/codex` |
| Single-route `remote-proxy` target | `--target https://proxy.example.com/antigravity` (or `/v1`, `/codex`, `/opencode-go`) | Automatically scoped to that single provider (same as setting `--provider`) |
| Single provider, custom HTTPS upstream (`usage-only`) | `--mode usage-only --provider openai --target https://proxy.example.com --upstream https://openrouter.ai/api/v1` | `openai` only → `https://openrouter.ai/api/v1`; usage → `https://proxy.example.com/api/usage/events` |
| Legacy Antigravity custom upstream (`usage-only`) | `--mode usage-only --target https://proxy.example.com --upstream https://upstream.example.com` | Scoped to `antigravity` → `https://upstream.example.com` |
| **Rejected** combinations | `--mode remote-proxy --upstream ...`<br>`--mode usage-only --target https://proxy.example.com/v1`<br>`--provider codex --target https://proxy.example.com/v1`<br>`--mode usage-only --target https://proxy.example.com --upstream https://proxy.example.com/v1` | Startup fails with a validation error (see [Troubleshooting](#8-troubleshooting)). |

---

## 5. Connect Clients

### Quick comparison by client

| Client | Direct to server | Via local relay (`127.0.0.1:8765`) | Upstream credentials |
| --- | --- | --- | --- |
| **OpenAI-compatible SDK/CLI** | Base URL `https://proxy.example.com/v1` + `X-LLM-Proxy-Key` header | Base URL `http://127.0.0.1:8765/v1` (no proxy header) | Normal API key (`Authorization` / API key header) |
| **OpenCode (`openai`, API key)** | `settings.baseURL: "https://proxy.example.com/v1"` + `headers` | `settings.baseURL: "http://127.0.0.1:8765/v1"` (no `headers`) | Saved OpenAI API key |
| **OpenCode (`openai`, ChatGPT sign-in)** | `settings.baseURL: "https://proxy.example.com/codex"` + `headers` | `settings.baseURL: "http://127.0.0.1:8765/codex"` (no `headers`) | Saved ChatGPT sign-in |
| **OpenCode Go (`opencode-go`)** | `settings.baseURL: "https://proxy.example.com/opencode-go"` + `headers` | `settings.baseURL: "http://127.0.0.1:8765/opencode-go"` (no `headers`) | Saved OpenCode Go credentials |
| **Signed-in Codex** | `base_url = "https://proxy.example.com/codex"` + `env_http_headers` | `base_url = "http://127.0.0.1:8765/codex"` (omit `env_http_headers`) | Existing ChatGPT sign-in (`requires_openai_auth = true`) |
| **Signed-in Antigravity** | Base URL `https://proxy.example.com/antigravity` + `X-LLM-Proxy-Key` (if client supports custom headers) | T3 launcher passing `--cloud_code_endpoint http://127.0.0.1:8765` | Existing Google sign-in |

### 5.1 OpenAI-compatible clients and OpenCode / OpenCode Go

#### Generic OpenAI-compatible clients

- **Direct:** Set the OpenAI base URL to `https://proxy.example.com/v1` (or `http://localhost:8080/v1` for local testing) and attach `X-LLM-Proxy-Key: <your-proxy-key>` alongside your normal `Authorization: Bearer <provider-api-key>` header.
- **Via relay:** Set the OpenAI base URL to `http://127.0.0.1:8765/v1` and keep your normal `Authorization` (or `X-API-Key` / `Api-Key` / `X-Goog-Api-Key`) header. Do not add `X-LLM-Proxy-Key`.

#### OpenCode (`opencode.jsonc`)

These examples use OpenCode v2 provider settings. [OpenCode provider configuration](https://opencode.ai/docs/providers/)

For direct requests, set `LLM_PROXY_KEY` in OpenCode's environment.

```jsonc
{
  "providers": {
    "openai": {
      // Use /v1 with an OpenAI API key, or /codex with ChatGPT sign-in.
      "settings": { "baseURL": "https://proxy.example.com/v1" },
      "headers": { "X-LLM-Proxy-Key": "{env:LLM_PROXY_KEY}" }
    },
    "opencode-go": {
      "settings": { "baseURL": "https://proxy.example.com/opencode-go" },
      "headers": { "X-LLM-Proxy-Key": "{env:LLM_PROXY_KEY}" }
    }
  }
}
```

For requests through the local relay, set the local base URLs. The relay supplies the proxy key.

```jsonc
{
  "providers": {
    "openai": {
      // Use /v1 with an OpenAI API key, or /codex with ChatGPT sign-in.
      "settings": { "baseURL": "http://127.0.0.1:8765/v1" }
    },
    "opencode-go": {
      "settings": { "baseURL": "http://127.0.0.1:8765/opencode-go" }
    }
  }
}
```

OpenCode continues using its saved upstream credentials for each provider.

### 5.2 Signed-in Codex

Edit the user `config.toml` read by the Codex process, including the process that T3 launches, then start a fresh session. [Codex configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)

For direct requests, supply `LLM_PROXY_KEY` in the Codex process environment.

```toml
model_provider = "metered-codex"

[model_providers.metered-codex]
name = "OpenAI"
base_url = "https://proxy.example.com/codex"
wire_api = "responses"
requires_openai_auth = true
supports_websockets = true
env_http_headers = { "X-LLM-Proxy-Key" = "LLM_PROXY_KEY" }
```

For requests through the local relay, select its local base URL. The relay supplies the proxy key.

```toml
model_provider = "metered-codex"

[model_providers.metered-codex]
name = "OpenAI"
base_url = "http://127.0.0.1:8765/codex"
wire_api = "responses"
requires_openai_auth = true
supports_websockets = true
```

- WebSockets (`supports_websockets = true`) are metered in direct, `remote-proxy`, and `usage-only` modes.
- The dashboard displays remaining Codex allowance windows whenever the upstream reports recent rate-limit headers or WebSocket `codex.rate_limits` events.

### 5.3 Signed-in Antigravity and T3 Code launcher

Keep Antigravity signed in to Google and keep both Google's token and your proxy key out of URLs, command arguments, and logs.

- **If your Antigravity client supports custom headers (direct):** Set the Cloud Code Assist base URL to `https://proxy.example.com/antigravity` and send `X-LLM-Proxy-Key` alongside Google's `Authorization` header.
- For the tested T3 Code Antigravity setup, run `llm-meter-relay` on the machine hosting the ACP process. Create an executable launcher named `antigravity` or `agy`.

```sh
#!/bin/sh
if [ "$1" = acp ]; then
  shift
  set -- acp --cloud_code_endpoint http://127.0.0.1:8765 "$@"
fi
exec "/absolute/path/to/antigravity" "$@"
```

1. Replace `/absolute/path/to/antigravity` with your installed Antigravity executable path.
2. Save the launcher script (named `antigravity` or `agy`) and make it executable with `chmod +x`.
3. Confirm your runtime supports the flag with `antigravity acp --help`.
4. In T3 Code, set **Settings → Providers → Antigravity → Binary path** to the launcher script on the environment that runs Antigravity.
5. Start a fresh Antigravity session using your existing Google sign-in.

The dashboard labels these requests as **Antigravity** under the client owning the proxy key and records token counts whenever Google reports them.

---

## 6. Security, Networking, Permissions, and Durable Queue Behavior

### 6.1 Docker loopback binding, private networking, and request guard

- **Bind published ports to loopback only:** Always publish `"127.0.0.1:8765:8765"` (never `"8765:8765"`). The relay itself injects your proxy key and does not require a proxy key from local clients, so exposing it on non-loopback interfaces would allow other hosts to attribute requests to your client.
- **Use Docker Engine 28 or newer** and keep the relay on a dedicated private Compose network (`networks: { relay: {} }`). Attach only trusted clients to that network, because containers sharing the network can reach the relay directly.
- **`--listen` vs. `--bind` in Docker:** Inside a container, `--bind=0.0.0.0:8765` listens on the container socket so Docker port forwarding works, while `--listen` (default `127.0.0.1:8765`) specifies the host-facing loopback address checked against each request's `Host` header. If you map a different host port such as `127.0.0.1:9000:8765`, pass `--listen=127.0.0.1:9000 --bind=0.0.0.0:8765`.
- **Built-in relay request guard:** Before forwarding any request, the relay rejects:
  - Any `Host` header that does not match `--listen` (`403 Forbidden`).
  - Any request containing `Origin` or `Sec-Fetch-Site` headers (`403 Forbidden`, mitigating cross-site browser requests to loopback).
  - HTTP `CONNECT` requests, unknown routes, and path traversal segments (`.`, `..`, `\`) (`404 Not Found`).
  - Requests missing upstream provider authorization (`401 Unauthorized`).

### 6.2 Key file permissions and container user mapping

The relay enforces strict checks when opening `--key-file`:

- Must exist and be a **regular file** (not a directory or pipe).
- On Linux and macOS, group and world permission bits must all be zero (`mode & 0077 == 0`, e.g., `0600` or `0400`).
- Must contain a single non-empty key of at most 4,096 bytes with printable ASCII characters and no internal whitespace (a trailing newline is allowed and trimmed).

**Container UID/GID choices:**

- **Recommended (default Compose example):** Run with `user: "${RELAY_UID}:${RELAY_GID}"` matching the host owner of `proxy.key` and `data`, and set `bind: { create_host_path: false }` so Docker fails fast instead of silently creating an empty directory if a path is missing.
- **Alternative (built-in image user):** Omit `user:` to run as the image's default `llmproxy` user (`10001:10001`), and `chown 10001:10001` on `proxy.key` (`0600`) and `data` (`0700`) on the host.

### 6.3 When `/data` is needed

| Component / mode | Is persistent `/data` needed? | What is stored there |
| --- | --- | --- |
| **LLM Meter server** | **Always** (`DATA_DIR=/data` in Docker, `./data` natively) | `llm-proxy.db` — clients, hashed proxy keys, request/usage ledger, allowances, and cached pricing catalogs. |
| **Relay (`remote-proxy`)** | **No** (not opened or written) | Nothing. Mounting `/data` in the default Compose file is harmless and makes switching to `usage-only` a one-line change. |
| **Relay (`usage-only`)** | **Yes** (`--data-dir`) | `usage-outbox.db` — durable SQLite queue of pending usage events plus the destination `(endpoint, key)` fingerprint. |

> **One `--data-dir` per `(server, key)` connection:** On first open, `usage-outbox.db` binds itself to a SHA-256 fingerprint of the upload endpoint (`https://<target>/api/usage/events`) and the proxy key. Starting a relay with a different `--target` or proxy key against the same directory fails immediately (`use a separate usage queue directory for a different server or key`) so pending events are never uploaded under the wrong client or server.

### 6.4 Durable queue, retry behavior, and graceful shutdown (`usage-only`)

- The relay queues metered HTTP requests, WebSocket usage, and quota observations in `usage-outbox.db`. Each event has a stable ID and a 16 KiB size limit.
- **Queue capacity:** Up to **10,000** pending events. If the queue reaches 10,000 events during an extended server outage, new events are dropped with log message `Could not queue a usage report`, while live model traffic to providers continues uninterrupted.
- **Ordered delivery & deduplication:** A single background worker uploads events strictly in FIFO order via `POST https://<target>/api/usage/events` with `X-LLM-Proxy-Key` and a 10-second timeout. The server returns `204 No Content` and deduplicates retries atomically by `(client_id, event_id)`, so retries never double-count.
- The uploader sends requests to the configured HTTPS URL. A redirect response leaves the event queued for retry at that URL.
- **Retry backoff vs. permanent rejection:**
  - **Retryable errors** (network failure, timeout, `401`, `404`, `405`, `429`, `5xx`, redirects): event stays queued; relay logs `Usage event remains queued` and backs off exponentially from **1 second** up to **60 seconds** (`1s → 2s → 4s → ... → 60s`), resetting to 1s after a successful upload.
  - **Permanent rejections** (`400 Bad Request`, `413 Payload Too Large`, `422 Unprocessable Entity`): the rejected event is deleted so it cannot block subsequent events; relay logs `Discarded usage event rejected by the server`.
- On `SIGINT` or `SIGTERM`, the relay stops uploading, cancels open WebSocket connections, and gives HTTP and WebSocket handlers up to 10 seconds to finish recording usage. Those reports can upload on the next start. Set `stop_grace_period: 15s` in Docker Compose to give the relay time to shut down.

---

## 7. Lifecycle, Upgrades, and Key Rotation

### Upgrading from LLM Proxy

The repository moved to [`mishankov/llm-meter`](https://github.com/mishankov/llm-meter). To update an existing checkout, run:

```sh
git remote set-url origin https://github.com/mishankov/llm-meter.git
```

LLM Meter continues publishing the `ghcr.io/mishankov/llm-proxy` and `ghcr.io/mishankov/llm-proxy-relay` image names. To switch to `llm-meter` and `llm-meter-relay`, update the image reference in your existing deployment. Keep the Compose service name, the server's `/data` mount, and the relay's key and outbox mounts. The examples for new installs use `~/.config/llm-meter/relay`; existing relays can keep their current host paths.

For Codex or OpenCode with ChatGPT sign-in, change the client base URL from `/backend-api/codex` to `/codex`. If your remote-proxy relay specifies the old route in `--target`, update that path as well. Drain any pending usage-only reports before upgrading the server and relay. Clients continue using their existing credentials and `X-LLM-Proxy-Key` header.

### Upgrading the LLM Meter server

Upgrade the server **before** enabling `usage-only` on any relay.

1. Keep the existing `/data` volume and `DASHBOARD_TOKEN`.
2. Pull the new image and recreate the container (or replace the native binary and restart):

   ```sh
   docker compose pull llm-meter
   docker compose up -d llm-meter
   ```

3. Confirm the server is healthy.

   ```sh
   curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz
   # Expect: 204
   ```

SQLite schema additions (such as `measurement_source`, `relay_events`, and `relay_completions`) apply automatically on startup.

### Upgrading the relay

```sh
export RELAY_UID="$(id -u)" RELAY_GID="$(id -g)"
docker compose pull relay
docker compose up -d relay
```

For native binaries, rebuild with `go build -o llm-meter-relay ./cmd/llm-proxy-relay` and restart the process. In `usage-only` mode, keeping the same `--data-dir` preserves any queued events across the restart.

### Check for pending reports

Pause new client requests and let the relay upload the backlog. If you have the `sqlite3` CLI, check the number of pending reports using the path to your relay state directory. The following command uses the default Compose setup in this guide.

```sh
sqlite3 -readonly "$HOME/.config/llm-meter/relay/data/usage-outbox.db" 'SELECT COUNT(*) FROM events;'
```

A result of `0` means the queue is empty. If it stays above zero, check the relay logs for upload errors before rotating the key or changing modes.

### Switching relay modes

- **`remote-proxy` → `usage-only`:**
  1. Confirm the server has been upgraded to support `POST /api/usage/events`.
  2. Ensure `--target` is the server origin only (no `/v1`, `/antigravity`, `/codex`, or `/opencode-go` path).
  3. Ensure `--data-dir` points to a persistent directory.
  4. Add `--mode=usage-only` and restart the relay.
- **`usage-only` → `remote-proxy`:**
  1. [Check for pending reports](#check-for-pending-reports) and wait for the queue to empty.
  2. Remove `--mode=usage-only` and restart the relay.

### Rotating proxy keys or moving to another server (`usage-only`)

Use a fresh queue directory when changing the proxy key or destination. The existing queue is bound to its original server and key.

1. Create a **new key** in the dashboard (do not revoke the old key yet).
2. [Check for pending reports](#check-for-pending-reports) and let the relay upload them using its current key and server.
3. Stop the relay (`docker compose stop relay`).
4. Replace the single line in `proxy.key` (keeping `chmod 600`) and update `--target` if moving servers.
5. Point `--data-dir` at a fresh directory. In Compose, create a new host directory and update the source for the `/data` mount.
6. Start the relay, send a test request, confirm it appears in the dashboard, and then revoke the old key in **Clients**.

---

## 8. Troubleshooting

### Server startup and dashboard access

| Symptom / Error | Likely cause | Fix |
| --- | --- | --- |
| `DASHBOARD_TOKEN must contain 24 to 512 characters` | Missing or too-short token | Generate one with `openssl rand -hex 32` and pass via `DASHBOARD_TOKEN`. |
| `COOKIE_SECURE must be true or false` | Invalid boolean value | Set `COOKIE_SECURE=true` (HTTPS) or `COOKIE_SECURE=false` (local HTTP). |
| `<NAME>_UPSTREAM must be an HTTP or HTTPS base URL without credentials, query, or fragment` | Malformed upstream override | Remove any `user:pass@`, `?query`, or `#fragment` and include `http://` or `https://`. |
| `dashboard build unavailable` (native run) | Built without `-tags embedui` and `web/build/index.html` is missing | Run `npm ci --prefix web && npm run build --prefix web`, set `DASHBOARD_DIR`, or build with `-tags embedui`. |
| Dashboard prompts to unlock again right after sign-in | Running over plain `http://` while `COOKIE_SECURE=true` | Set `COOKIE_SECURE=false` for local `http://localhost:8080`, or access over HTTPS via your reverse proxy. |
| `401 A valid X-LLM-Proxy-Key is required` on proxy requests | Sent `DASHBOARD_TOKEN`, missing/duplicate `X-LLM-Proxy-Key` header, or revoked key | Create a client key (`llmp_...`) in **Clients** and send exactly one `X-LLM-Proxy-Key` header. |

### Relay startup errors

| Error message | Likely cause | Fix |
| --- | --- | --- |
| `--target must be an HTTPS LLM Meter URL without user info, query, or fragment` | Used `http://`, or included credentials/query/fragment | Use an `https://` origin such as `https://proxy.example.com`. |
| `--target must be an HTTPS LLM Meter origin in usage-only mode` | Included a route like `/antigravity` or `/v1` in `--target` with `--mode usage-only` | Strip the path so `--target` is only `https://proxy.example.com`; use `--provider` if you want to restrict routes. |
| `--target must be an HTTPS LLM Meter origin or a matching provider route` | Path on `--target` is not a valid provider route or disagrees with `--provider` | Use the server origin only, or make `--provider` and the `--target` path match. |
| `--upstream requires --mode usage-only` | Passed `--upstream` in `remote-proxy` mode | Add `--mode usage-only`, or set `*_UPSTREAM` on the LLM Meter server instead. |
| `the provider and LLM Meter must use different hosts in usage-only mode` | `--upstream` (or default provider upstream) shares a hostname with `--target` | Point `--target` at your LLM Meter server host and `--upstream` at the provider host. |
| `--listen must specify a loopback IP and port` / `--bind requires 0.0.0.0 and a fixed port...` | Non-loopback `--listen`, ephemeral port `0`, or non-`0.0.0.0` `--bind` | Use `127.0.0.1:<port>` or `[::1]:<port>` for `--listen`; in Docker pair it with `--bind=0.0.0.0:<port>`. |
| `cannot open the proxy key file` / `must be a regular file` / `must be accessible only to its owner` / `must contain a single key` | Wrong path, directory mounted instead of file, loose permissions, container UID mismatch, or extra lines/whitespace | Verify `proxy.key` exists as a regular single-line file, run `chmod 600 proxy.key`, and match `RELAY_UID`/`RELAY_GID` (or `10001:10001`). |
| `use a separate usage queue directory for a different server or key` | Reused a `--data-dir` already bound to another `--target` or proxy key | Drain pending events with the old settings if needed, then use a fresh empty `--data-dir`. |

### Client requests through the relay

| HTTP status / Response | Likely cause | Fix |
| --- | --- | --- |
| `403 Forbidden` | Request `Host` header does not match `--listen` (e.g., client used `localhost:8765` instead of `127.0.0.1:8765`, or host port mapping differs from `--listen`), or request included `Origin` / `Sec-Fetch-Site` | Point clients at `http://127.0.0.1:<port>` matching `--listen`, and do not call the relay from browser contexts sending `Origin`/`Sec-Fetch-Site`. |
| `401 Google authorization is required` / `401 Provider authorization is required` | Client did not send its upstream auth header | Confirm the client is signed in (Google for Antigravity, ChatGPT for Codex) or configured with its provider API key. |
| `404 Not Found` | Wrong base URL path (e.g., `/antigravity` on the relay instead of root `http://127.0.0.1:8765`), or route excluded by `--provider` / legacy `--target` | Check [Section 3](#all-four-client-base-paths) and verify `--provider` / `--target` scope. |
| `502 LLM Meter unavailable` (`remote-proxy`) | Relay cannot connect to the LLM Meter server over HTTPS | Check DNS, TLS certificate trust, firewall/VPN, and server `/healthz`. |
| `502 Upstream unavailable` (`usage-only` or direct server) | Provider upstream is unreachable or refused the connection | Check outbound HTTPS connectivity and any `--upstream` / `*_UPSTREAM` override. |

### `usage-only` uploads, pricing, and correlation

| Symptom / Log message | Likely cause | Fix |
| --- | --- | --- |
| `Usage event remains queued` (`HTTP 404` or `HTTP 405`) | LLM Meter server does not yet support `POST /api/usage/events` | Upgrade the LLM Meter server; queued events drain automatically once the updated server is running. |
| `Usage event remains queued` (`HTTP 401`) | Proxy key in `proxy.key` is invalid or revoked | Verify the key matches an active client key in **Clients** (follow [key rotation](#rotating-proxy-keys-or-moving-to-another-server-usage-only) if replacing it). |
| `Usage event remains queued` (`HTTP 301`/`302`/`307`/`308`) | Reverse proxy is redirecting `POST /api/usage/events` | Point `--target` at the canonical HTTPS hostname; the relay intentionally does not follow redirects. |
| `Could not queue a usage report` | Queue hit the 10,000-event cap during an outage, or `--data-dir` is unwritable/full | Restore connectivity to the server so the queue drains; verify `--data-dir` ownership (`RELAY_UID`/`RELAY_GID`) and free disk space. |
| `Discarded usage event rejected by the server` (`400`/`413`/`422`) | Server rejected the payload (version mismatch or host clock more than 10 minutes ahead of server) | Synchronize system clocks (NTP) and keep relay and server versions aligned. |
| Requests appear as **Unpriced** | Provider omitted cost and model/category lacks a rate, or Models.dev flags separate audio rates | Check server logs for pricing feed refresh errors, ensure one Models.dev refresh succeeds after upgrading, or add complete rates for the model in `PRICING_FILE`. |
| Background response completion tokens missing on original request | Creation (`POST /v1/responses`) and polling (`GET /v1/responses/<id>`) used different dashboard clients or mixed `proxy` and `relay` modes | Route creation and polling through the same dashboard client and mode. |
