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

All setups feed the same dashboard ledger and use dashboard client proxy keys (`llmm_...`) for attribution.

| Setup | Model traffic path | What the LLM Meter server sees | Client custom header (`X-LLM-Meter-Key`) needed? | Local relay `/data` state needed? | Request `measurementSource` |
| --- | --- | --- | --- | --- | --- |
| **Direct client-to-server** | Client → LLM Meter → Provider | Full proxied requests, responses, and upstream auth headers | Yes | No | `proxy` |
| **Direct OpenTelemetry (`/otlp`)** | Harness → Model Provider. The harness exports telemetry directly to LLM Meter (`/otlp`). | Full exported telemetry, including any captured content. The server stores only extracted usage metadata. | Yes | No | `relay` |
| **Relay (`remote-proxy`, default)** | Client → Local Relay → LLM Meter → Provider | Full proxied requests, responses, and upstream auth headers | Relay supplies it | Optional unless OpenTelemetry is also enabled | `proxy` |
| **Relay (`usage-only`)** | Client → Local Relay → Provider. Relay reports to LLM Meter. | Usage metadata only (model, canonical route, status, tokens, and costs) | Relay supplies it for uploads | Yes (`usage-outbox.db` SQLite queue) | `relay` |
| **Relay (OpenTelemetry)** | Harness → Model Provider. The harness exports telemetry to the local relay. | Usage metadata (model, tokens, and available cost estimates) | Relay supplies it for uploads | Yes (`usage-outbox.db` SQLite queue) | `relay` |

### When to pick each setup

- **Choose direct client-to-server** for generic OpenAI-compatible clients or Antigravity clients that can send custom headers. It supports server-side upstream overrides without a local relay process.
- **Choose direct OpenTelemetry (`/otlp`)** to meter Claude Code, Codex, Gemini CLI, GitHub Copilot CLI, or OpenCode directly against the LLM Meter server without running a local relay process.
- **Choose relay in `remote-proxy` mode** to route generic OpenAI-compatible clients or Antigravity through the server when those clients can set an endpoint but cannot send the LLM Meter header.
- **Choose relay in `usage-only` mode** to forward requests from generic OpenAI-compatible clients or Antigravity directly to the provider from your machine and upload usage metadata to the dashboard.
- **Enable the relay's OpenTelemetry listener** when you want local content filtering before telemetry leaves your machine, durable offline buffering (`usage-outbox.db`), or OpenTelemetry ingestion for a client that cannot send custom headers. It can run alongside either proxy mode.

In `usage-only` mode, the server authenticates uploads with the dashboard client's proxy key and trusts that client to report its own usage. Prompt and response content and provider credentials travel between the client, relay, and provider. Uploads contain canonical route templates such as `/v1/responses/:id`, hashed response IDs, token counts, request outcomes, and reported costs.

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

The image runs as non-root user `llmmeter` (UID/GID `10001`), sets `DATA_DIR=/data`, exposes port `8080`, declares `VOLUME ["/data"]`, and embeds the built dashboard UI (`-tags embedui`).

### Docker with `docker run`

For local testing, start the server on loopback with HTTP.

```sh
export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
docker run --rm -p 127.0.0.1:8080:8080 \
  -e DASHBOARD_TOKEN \
  -e COOKIE_SECURE=false \
  -v llm-meter-data:/data \
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
      # ANTIGRAVITY_UPSTREAM: https://daily-cloudcode-pa.googleapis.com
    volumes:
      - llm-meter-data:/data
      # - ./pricing.json:/config/pricing.json:ro

volumes:
  llm-meter-data:
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
| `DASHBOARD_TOKEN` | _(none)_ | Yes | 24–512 characters. Unlocks the dashboard UI/API (`POST /api/login`) and signs the `llm_meter_session` cookie. Never valid as a proxy client key (`X-LLM-Meter-Key`). |
| `COOKIE_SECURE` | `true` | No | `true` or `false`. Keep `true` behind HTTPS. Set `false` only for plain local HTTP (`http://localhost:8080`). |
| `LISTEN_ADDR` | `:8080` | No | TCP listen address (for example `127.0.0.1:8080` or `:8080`). |
| `DATA_DIR` | `./data` (`/data` in Docker) | No | Stores `llm-meter.db` (SQLite in WAL mode). The server creates a missing directory with mode `0700`. |
| `PRICING_FILE` | _(unset)_ | No | Path to a JSON file of per-model rate overrides in USD per million tokens. |
| `OPENAI_UPSTREAM` | `https://api.openai.com/v1` | No | HTTP/HTTPS base URL for `/v1` requests. Must omit userinfo, query, and fragment. |
| `ANTIGRAVITY_UPSTREAM` | `https://daily-cloudcode-pa.googleapis.com` | No | HTTP/HTTPS base URL for `/antigravity` requests. Must omit userinfo, query, and fragment. |
| `DASHBOARD_DIR` | `web/build` | No | Path to static dashboard assets containing `index.html`. Used **only** when built without `-tags embedui`; ignored in Docker and `-tags embedui` binaries. |

#### Upstream override behavior

- **`OPENAI_UPSTREAM` provider detection:** When you point `OPENAI_UPSTREAM` at another OpenAI-compatible host, LLM Meter infers the provider label for pricing lookup from known hostnames: `api.openai.com` (`openai`), `openrouter.ai` (`openrouter`), `opencode.ai/inference/go` (`opencode-go`), `opencode.ai/inference` (`opencode`), `api.anthropic.com` (`anthropic`), `api.deepseek.com` (`deepseek`), `api.groq.com` (`groq`), `api.mistral.ai` (`mistral`), `api.x.ai` (`xai`), and `generativelanguage.googleapis.com` (`google`). Unknown hosts are recorded without a provider label and can be priced via `PRICING_FILE`.
- **In `usage-only` mode:** Set `--provider` and `--upstream` on the relay to override the base URL used for direct provider requests. [Provider scoping](#46-provider-scoping-and-https---upstream-overrides)

### Pricing feeds and `PRICING_FILE`

The server checks these sources in order when recording a request's cost in USD.

1. **Provider-reported amounts** (including `0`): exact reported cost, provider estimate, or provider list cost. Historical OpenCode Go records retain any reported charge, list value, and allowance use.
2. **`PRICING_FILE` overrides**: checked first as `"<provider>/<model>"` (for example `"openai/my-model"`), then as `"<model>"`. For Antigravity requests, the order is `"google/<model>"`, the legacy `"antigravity/<model>"`, then `"<model>"`.
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

Include a rate for every token category a request reports. Use `0` when a category is free. When a request has no provider-reported cost or applicable override, automatic pricing can leave it **Unpriced** if a rate is missing or the catalog lists separate audio rates.

---

## 3. Proxy Keys, Authentication, and Client Base Paths

### Creating and scoping proxy keys

1. Open the dashboard, unlock it with `DASHBOARD_TOKEN`, and go to **Clients**.
2. Create a client and copy its proxy key (`llmm_...`). The full key is displayed **only once**; the server stores only its SHA-256 digest and 13-character prefix.
3. Keep multiple active keys for the same client during rotation. Revoke the old key once the replacement is in use.

Open a client’s details in **Clients** to use **Rename** or **Archive**. Renaming changes its label throughout the dashboard, including past usage. Existing proxy keys continue to work. Archiving removes the client from **Clients** and revokes all its proxy keys. Past usage stays available in **Usage**. If the relay uses Usage-only or OpenTelemetry, let it upload pending reports before archiving the client.

**Key scope:**

- One proxy key authenticates proxy routes (`/v1` and `/antigravity`), direct OpenTelemetry ingestion (`/otlp/v1/logs`, `/otlp/v1/traces`, and `/otlp/v1/metrics`), and the usage ingestion endpoint (`POST /api/usage/events`).
- All requests using keys belonging to the same dashboard client are grouped under that client. Provider labels include `openai`, `codex`, `opencode-go`, and `google` for Antigravity.
- Create **one client** per workstation/relay if you want one combined client view, or **separate clients** (with separate keys, and separate relay instances if using the relay) if you want per-tool or per-provider attribution in **Clients** and **Usage**.
- **Background responses (`background: true`):** Keep both the initial `POST /v1/responses` and follow-up `GET /v1/responses/<id>` polls on the **same dashboard client and measurement mode** (`proxy` vs `relay`) so the server can correlate completion usage onto the originating request.

### Normal provider auth vs. `X-LLM-Meter-Key`

| Connection path | What the client sends | What LLM Meter / Relay does with headers |
| --- | --- | --- |
| **Direct to server (proxy)** | Client's normal upstream auth (`Authorization`, API key, etc.) **plus** one `X-LLM-Meter-Key` header | Server validates `X-LLM-Meter-Key`, strips `X-LLM-Meter-Key` (and any `llm_meter_session` cookie), and forwards the client's upstream auth to the provider. |
| **Direct to server (OpenTelemetry)** | Harness sends OTLP/HTTP to `/otlp` with one `X-LLM-Meter-Key` header and authenticates directly with its model provider. | Server validates and strips `X-LLM-Meter-Key`, extracts usage metadata, discards content fields, and deduplicates retries by `(client_id, event_id)`. |
| **Via relay (`remote-proxy`)** | Client's normal upstream auth only (no `X-LLM-Meter-Key` needed) | Relay requires provider auth before forwarding, sets/replaces `X-LLM-Meter-Key` from `--key-file`, and forwards over HTTPS to LLM Meter. |
| **Via relay (`usage-only`)** | Client's normal upstream auth only (no `X-LLM-Meter-Key` needed) | Relay requires provider auth, strips any inbound `X-LLM-Meter-Key`, forwards directly over HTTPS to the provider, and sends `--key-file` only in `X-LLM-Meter-Key` to `POST /api/usage/events`. |
| **Via relay (OpenTelemetry)** | The harness sends OpenTelemetry to the local receiver and authenticates directly with its model provider. | Relay filters usage and uses the `--key-file` key to authenticate uploads to `POST /api/usage/events`. |

### Proxy client base paths

| Provider | Direct to LLM Meter server | Via local relay (`127.0.0.1:8765`) | Server path (`remote-proxy`) | Provider auth accepted by relay | Default upstream |
| --- | --- | --- | --- | --- | --- |
| **OpenAI-compatible** (`openai`) | `https://proxy.example.com/v1` | `http://127.0.0.1:8765/v1` | `/v1` | `Authorization`, `X-API-Key`, `Api-Key`, or `X-Goog-Api-Key` | `https://api.openai.com/v1` |
| **Signed-in Antigravity** (`antigravity`) | `https://proxy.example.com/antigravity` | `http://127.0.0.1:8765/antigravity` | `/antigravity` | `Authorization` (Google sign-in only) | `https://daily-cloudcode-pa.googleapis.com` |

> **Note on Antigravity's local relay URL:** Both the server and the local relay serve Antigravity under `/antigravity` (matching Cloud Code RPC paths `/antigravity/v1internal:<Method>`). In `remote-proxy` mode the relay forwards to `/antigravity/v1internal:<Method>` on the server; in `usage-only` mode it sends `/v1internal:<Method>` directly to the Antigravity upstream.
>
> Use `127.0.0.1` in client URLs with the default listener. The relay checks the request's `Host` against `--listen`. Using `localhost:8765` with the default listener returns `403 Forbidden`.

---

## 4. Run the Local Relay

The proxy listens at `http://127.0.0.1:8765` by default. It serves the OpenAI-compatible and Antigravity routes in either proxy mode. Enable a separate OTLP/HTTP listener with `--telemetry-listen=127.0.0.1:4318`. Set `--target` to your LLM Meter server's HTTPS origin (`https://proxy.example.com`) and point `--key-file` at a private file containing one dashboard client key for both listeners.

### 4.1 Prepare the key file and state directory

Create a private directory and key file with owner-only permissions.

```sh
relay_dir="$HOME/.config/llm-meter/relay"
(umask 077; mkdir -p "$relay_dir/data"; touch "$relay_dir/proxy.key")
chmod 600 "$relay_dir/proxy.key"
```

Write your dashboard client proxy key (`llmm_...`) as the **only line** in `$HOME/.config/llm-meter/relay/proxy.key` (no extra lines, spaces, or comments).

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

- **To switch this service to `usage-only` mode**, add `- --mode=usage-only` under `command` and re-run `docker compose up -d relay`.
- **For OpenTelemetry alongside the proxy**, add `127.0.0.1:4318:4318` under `ports` and add `--telemetry-listen=127.0.0.1:4318` and `--telemetry-bind=0.0.0.0:4318` under `command`. Follow [the exporter setup](#53-opentelemetry).
- **For OpenTelemetry only**, make the same additions, add `--proxy=false`, and remove the proxy's port mapping and `--bind` flag.
- **For HTTPS on the telemetry listener**, mount the TLS certificate and private key read-only and point `--tls-cert` and `--tls-key` at their container paths. The proxy uses its own HTTP listener.
- **Keep `/data` mounted** whenever Usage-only or OpenTelemetry is enabled so queued usage reports survive container recreation.

### 4.3 Equivalent native Go binary commands

Build the relay binary.

```sh
go build -o llm-meter-relay ./cmd/llm-meter-relay
```

- Run the relay for both proxy routes on `127.0.0.1:8765` in the default `remote-proxy` mode.

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

`--mode` selects how the proxy operates. The proxy and OpenTelemetry listener share the instance's `--key-file` and `--data-dir`. Run additional instances on separate loopback ports and give each instance that queues local usage its own `--data-dir`. Common reasons to run more than one instance:

- **Mixed modes** on the same machine — for example, `usage-only` for signed-in Antigravity on `127.0.0.1:8765` and `remote-proxy` for OpenAI-compatible tools on `127.0.0.1:8766`.
- **Separate dashboard clients** — different `--key-file` files so each tool or provider appears under its own client name in the dashboard.
- **Per-provider `--upstream` override** in `usage-only` mode — since `--upstream` scopes an instance to one provider.

These commands run two isolated native instances. Run each in its own terminal.

```sh
# Instance 1: usage-only for Antigravity on 127.0.0.1:8765
./llm-meter-relay \
  --mode usage-only \
  --listen 127.0.0.1:8765 \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/antigravity.key" \
  --data-dir "$HOME/.config/llm-meter/antigravity-data"

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
| `--target` | _(none)_ | Yes | HTTPS LLM Meter origin, without a route, user info, query, or fragment. |
| `--key-file` | _(none)_ | Yes | Path to a private regular file containing a single proxy key (max 4,096 bytes, one line, printable ASCII, no whitespace). Must be owner-only (`0600`/`0400`) on Linux/macOS. |
| `--proxy` | `true` | No | Set `--proxy=false` with `--telemetry-listen` to run only the OTLP/HTTP receiver. |
| `--mode` | `remote-proxy` | No | Choose `remote-proxy` or `usage-only` for the proxy. The existing `telemetry` value enables standalone OTLP/HTTP using `--listen` and `--bind`. |
| `--provider` | `""` (both proxy routes) | No | Restrict the proxy to `antigravity` or `openai`. |
| `--upstream` | `""` | No | Direct HTTPS provider base URL override (no user info, query, or fragment). Requires `--mode usage-only` and `--provider`. Hostname must differ from `--target`. |
| `--listen` | `127.0.0.1:8765` | No | Client-facing loopback IP and port (`127.0.0.1:<port>` or `[::1]:<port>`). Enforced against the incoming HTTP `Host` header. |
| `--bind` | `""` | No | Container socket address. When set, must be `0.0.0.0:<nonzero-port>` paired with a fixed non-zero loopback `--listen`. |
| `--telemetry-listen` | `""` (off) | No | Enable OTLP/HTTP at a client-facing loopback IP and port, for example `127.0.0.1:4318`. If the proxy is enabled, use a separate address or port. |
| `--telemetry-bind` | `""` | No | Container socket `0.0.0.0:<nonzero-port>` for a fixed loopback `--telemetry-listen`. |
| `--data-dir` | `./data/relay` | No | Persistent `usage-outbox.db` directory for Usage-only and OpenTelemetry. Give each relay instance its own queue directory. |
| `--tls-cert`, `--tls-key` | `""` | No | Supply both PEM files to enable HTTPS on the OpenTelemetry listener. Server uploads use the HTTPS `--target` in every mode. |

### 4.6 Provider scoping and HTTPS `--upstream` overrides

| Goal | Flags | Resulting routes & destination |
| --- | --- | --- |
| Both routes via LLM Meter (default) | `--target https://proxy.example.com` | Both routes → `https://proxy.example.com/{antigravity,v1}` |
| Both routes, direct to standard upstreams | `--mode usage-only --target https://proxy.example.com` | Model traffic goes to each provider's default upstream. Usage uploads go to `https://proxy.example.com/api/usage/events`. |
| Single provider via LLM Meter | `--provider antigravity --target https://proxy.example.com` | Antigravity only → `https://proxy.example.com/antigravity` |
| Single provider, custom HTTPS upstream (`usage-only`) | `--mode usage-only --provider openai --target https://proxy.example.com --upstream https://openrouter.ai/api/v1` | `openai` only → `https://openrouter.ai/api/v1`; usage → `https://proxy.example.com/api/usage/events` |

---

## 5. Connect Clients

### Quick comparison by client

| Client | Direct to server | Via local relay (`127.0.0.1:8765`) | Upstream credentials |
| --- | --- | --- | --- |
| **Claude Code, Codex, Gemini CLI, GitHub Copilot CLI, OpenCode** | [Direct OpenTelemetry](#53-opentelemetry) to `https://proxy.example.com/otlp` (or `http://localhost:8080/otlp`; Copilot CLI requires HTTPS) + `X-LLM-Meter-Key` header (model traffic goes directly to the provider) | [OpenTelemetry exporter](#53-opentelemetry) points to the local relay (`http://127.0.0.1:4318/otlp`) | Existing provider credentials |
| **Generic OpenAI-compatible SDK/CLI** | Base URL `https://proxy.example.com/v1` + `X-LLM-Meter-Key` header | Base URL `http://127.0.0.1:8765/v1` (no proxy header) | Normal API key (`Authorization` / API key header) |
| **Signed-in Antigravity** | Base URL `https://proxy.example.com/antigravity` + `X-LLM-Meter-Key` (if client supports custom headers) | T3 launcher passing `--cloud_code_endpoint http://127.0.0.1:8765/antigravity` | Existing Google sign-in |

### 5.1 Generic OpenAI-compatible clients

- **Direct:** Set the OpenAI base URL to `https://proxy.example.com/v1` (or `http://localhost:8080/v1` for local testing) and attach `X-LLM-Meter-Key: <your-proxy-key>` alongside your normal `Authorization: Bearer <provider-api-key>` header.
- **Via relay:** Set the OpenAI base URL to `http://127.0.0.1:8765/v1` and keep your normal `Authorization` (or `X-API-Key` / `Api-Key` / `X-Goog-Api-Key`) header. Do not add `X-LLM-Meter-Key`.

### 5.2 Signed-in Antigravity and T3 Code launcher

Keep Antigravity signed in to Google and keep both Google's token and your proxy key out of URLs, command arguments, and logs.

- **If your Antigravity client supports custom headers (direct):** Set the Cloud Code Assist base URL to `https://proxy.example.com/antigravity` and send `X-LLM-Meter-Key` alongside Google's `Authorization` header.
- For the tested T3 Code Antigravity setup, run `llm-meter-relay` on the machine hosting the ACP process. Create an executable launcher named `antigravity` or `agy`.

```sh
#!/bin/sh
if [ "$1" = acp ]; then
  shift
  set -- acp --cloud_code_endpoint http://127.0.0.1:8765/antigravity "$@"
fi
exec "/absolute/path/to/antigravity" "$@"
```

1. Replace `/absolute/path/to/antigravity` with your installed Antigravity executable path.
2. Save the launcher script (named `antigravity` or `agy`) and make it executable with `chmod +x`.
3. Confirm your runtime supports the flag with `antigravity acp --help`.
4. In T3 Code, set **Settings → Providers → Antigravity → Binary path** to the launcher script on the environment that runs Antigravity.
5. Start a fresh Antigravity session using your existing Google sign-in.

The dashboard shows **Google** as the provider under the client owning the proxy key and records token counts whenever Google reports them. The request details show the Antigravity endpoint. Continue to use this relay setup for Antigravity CLI and ACP. Google's documented OpenTelemetry tracing applies to the Antigravity SDK. [Antigravity settings](https://antigravity.google/docs/settings?tab=cli), [Antigravity SDK changelog](https://antigravity.google/docs/changelog)

### 5.3 OpenTelemetry

You can send OpenTelemetry (OTLP/HTTP) exports either **directly to the LLM Meter server at `/otlp`** or **through the local telemetry relay**. Both paths share the same parser, content stripping, and per-client `(client_id, event_id)` deduplication, so exporter retries never double-count.

If you previously metered Codex or OpenCode through a proxy, follow the [migration steps](#migrating-from-the-codex-and-opencode-go-proxy-routes) before enabling telemetry.

#### Option A: Direct OTLP/HTTP to the LLM Meter server (`/otlp`)

The server exposes authenticated OTLP/HTTP ingestion under the `/otlp` prefix so it never collides with `/v1` OpenAI proxy routes:

- **Local server:** `http://localhost:8080/otlp` (Copilot CLI requires HTTPS)
- **Remote server behind HTTPS:** `https://proxy.example.com/otlp`
- **Exact signal paths:** `POST /otlp/v1/logs`, `POST /otlp/v1/traces`, and `POST /otlp/v1/metrics` (acknowledged and discarded)
- **Authentication:** Send a single `X-LLM-Meter-Key: <your-proxy-key>` header on every request. For OTLP environment variables, set `OTEL_EXPORTER_OTLP_HEADERS="X-LLM-Meter-Key=<your-proxy-key>"` (or the signal-specific `OTEL_EXPORTER_OTLP_LOGS_HEADERS`).

Copy the exact `/otlp` endpoint for any client from **Clients → Connect this client → OpenTelemetry** in the dashboard.

Direct `/otlp` ingestion accepts JSON (`application/json`) and protobuf (`application/x-protobuf`) payloads, optional `Content-Encoding: gzip`, strips prompt/response content and non-usage telemetry, and writes extracted usage atomically to SQLite.

#### Option B: Local telemetry relay

Use the local telemetry relay instead of direct `/otlp` when you want content stripped on your machine before telemetry leaves it, durable offline buffering in `usage-outbox.db`, or OTLP ingestion for a client that cannot attach custom headers.

Update both the server and relay before enabling telemetry. Run the relay on the same machine as the harness.

On macOS, open the [desktop app](../desktop/README.md), enter the server's HTTPS origin and dashboard client key in **Settings**, and enable **OpenTelemetry** under **Local listeners**. Save with ⌘S, start the relay if it is stopped, and use ⌘⇧E in **Client Setup** to copy the endpoint. Use that endpoint URL (which ends in `/otlp`) in the exporter examples below.

```sh
./llm-meter-relay \
  --proxy=false \
  --telemetry-listen=127.0.0.1:4318 \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/relay/proxy.key" \
  --data-dir "$HOME/.config/llm-meter/relay/telemetry-data"
```

To enable the proxy alongside OpenTelemetry, omit `--proxy=false` and choose `--mode=usage-only` or the default `remote-proxy`. The two listeners share the upload destination, key, and queue. Existing `--mode=telemetry` commands still work. If you already run a telemetry relay, reuse its `--data-dir` with the same target and key so pending reports can finish uploading.

The local relay accepts OTLP over HTTP or HTTPS, with JSON or protobuf payloads, at `POST /otlp/v1/logs` and `POST /otlp/v1/traces`, and acknowledges and discards `POST /otlp/v1/metrics` — matching the server's `/otlp` paths. Harnesses can share the same receiver when they use its configured transport. Every report from an instance is attributed to the dashboard client that owns its key. For separate attribution, use a second loopback port, key, and data directory (or separate client keys when exporting directly to `/otlp`).

After a successful model request, look under **Usage** for that client. Exporters may batch reports. LLM Meter records model names, token counts, cache counts where available, and Claude Code's reported cost estimates. Other costs are estimated from the ledger's rates when available. Other telemetry including content fields is discarded.

#### Claude Code

Start a fresh session with its log exporter pointed directly at `/otlp/v1/logs` (replace `https://proxy.example.com/otlp` with `http://localhost:8080/otlp` for a local server):

```sh
(
  unset OTEL_LOG_RAW_API_BODIES OTEL_EXPORTER_OTLP_HEADERS
  export CLAUDE_CODE_ENABLE_TELEMETRY=1
  export OTEL_LOGS_EXPORTER=otlp OTEL_METRICS_EXPORTER=none OTEL_TRACES_EXPORTER=none
  export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf
  export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=https://proxy.example.com/otlp/v1/logs
  export OTEL_EXPORTER_OTLP_LOGS_HEADERS="X-LLM-Meter-Key=<your-proxy-key>"
  export OTEL_LOG_USER_PROMPTS=0 OTEL_LOG_ASSISTANT_RESPONSES=0
  export OTEL_LOG_TOOL_DETAILS=0 OTEL_LOG_TOOL_CONTENT=0
  claude
)
```

Or point it at the local telemetry relay (no custom header needed):

```sh
(
  unset OTEL_LOG_RAW_API_BODIES OTEL_EXPORTER_OTLP_HEADERS OTEL_EXPORTER_OTLP_LOGS_HEADERS
  export CLAUDE_CODE_ENABLE_TELEMETRY=1
  export OTEL_LOGS_EXPORTER=otlp OTEL_METRICS_EXPORTER=none OTEL_TRACES_EXPORTER=none
  export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf
  export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:4318/otlp/v1/logs
  export OTEL_LOG_USER_PROMPTS=0 OTEL_LOG_ASSISTANT_RESPONSES=0
  export OTEL_LOG_TOOL_DETAILS=0 OTEL_LOG_TOOL_CONTENT=0
  claude
)
```

LLM Meter reads Claude Code's `api_request` events. If usage is missing, check that the server or relay is running, the URL matches the configured OpenTelemetry address, and `X-LLM-Meter-Key` is valid for direct exports. Claude Code can log exporter errors to a private file with `claude --debug-file <path>`. On a managed installation, your administrator may control the export destination. [Claude Code monitoring documentation](https://code.claude.com/docs/en/monitoring-usage)

#### Codex

Add these settings to the user `config.toml` read by the Codex process, merging them into any existing `[otel]` table. Start a new session after saving.

Direct to the LLM Meter server (replace `https://proxy.example.com/otlp` with `http://localhost:8080/otlp` for a local server):

```toml
[otel]
exporter = { otlp-http = { endpoint = "https://proxy.example.com/otlp/v1/logs", protocol = "json", headers = { "X-LLM-Meter-Key" = "<your-proxy-key>" } } }
log_user_prompt = false
log_agent_responses = false
log_guardian_assessments = false
log_sse_deltas = false
tool_result.max_bytes = 0
```

Or via the local telemetry relay (no custom header needed):

```toml
[otel]
exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/otlp/v1/logs", protocol = "json" } }
log_user_prompt = false
log_agent_responses = false
log_guardian_assessments = false
log_sse_deltas = false
tool_result.max_bytes = 0
```

LLM Meter records each successful HTTP or WebSocket completion once and identifies subscription traffic when Codex reports ChatGPT sign-in. API-key and unknown sign-in modes carry token counts with the provider left unspecified. [Codex observability documentation](https://learn.chatgpt.com/docs/config-file/config-advanced#observability-and-telemetry)

#### Gemini CLI

Set Gemini CLI's exporter endpoint to the base OTLP URL; Gemini CLI appends `/v1/logs` and `/v1/metrics`.

Direct to the LLM Meter server (replace `https://proxy.example.com/otlp` with `http://localhost:8080/otlp` for a local server):

```sh
(
  unset GEMINI_TELEMETRY_OUTFILE GEMINI_TELEMETRY_OTLP_HEADERS OTEL_EXPORTER_OTLP_LOGS_HEADERS
  export GEMINI_TELEMETRY_ENABLED=true GEMINI_TELEMETRY_TARGET=local
  export GEMINI_TELEMETRY_OTLP_PROTOCOL=http
  export GEMINI_TELEMETRY_OTLP_ENDPOINT=https://proxy.example.com/otlp
  export OTEL_EXPORTER_OTLP_HEADERS="X-LLM-Meter-Key=<your-proxy-key>"
  export GEMINI_TELEMETRY_LOG_PROMPTS=false GEMINI_TELEMETRY_TRACES_ENABLED=false
  gemini
)
```

Or via the local telemetry relay:

```sh
(
  unset GEMINI_TELEMETRY_OUTFILE GEMINI_TELEMETRY_OTLP_HEADERS
  unset OTEL_EXPORTER_OTLP_HEADERS OTEL_EXPORTER_OTLP_LOGS_HEADERS
  export GEMINI_TELEMETRY_ENABLED=true GEMINI_TELEMETRY_TARGET=local
  export GEMINI_TELEMETRY_OTLP_PROTOCOL=http
  export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://127.0.0.1:4318/otlp
  export GEMINI_TELEMETRY_LOG_PROMPTS=false GEMINI_TELEMETRY_TRACES_ENABLED=false
  gemini
)
```

LLM Meter reads Gemini CLI's API response logs, including tool prompt tokens as input and thinking tokens as output. [Gemini CLI telemetry documentation](https://geminicli.com/docs/cli/telemetry/)

#### GitHub Copilot CLI

When exporting directly to an HTTPS LLM Meter server, point Copilot CLI at `https://proxy.example.com/otlp` with `X-LLM-Meter-Key` (no local loopback certificate needed):

```sh
COPILOT_OTEL_ENABLED=true \
COPILOT_OTEL_EXPORTER_TYPE=otlp-http \
OTEL_EXPORTER_OTLP_ENDPOINT=https://proxy.example.com/otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json \
OTEL_EXPORTER_OTLP_HEADERS="X-LLM-Meter-Key=<your-proxy-key>" \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false \
copilot
```

For a local relay (or local HTTPS setup), use a local HTTPS telemetry listener with a certificate valid for the loopback address. The commands below use `mkcert` to create a dedicated local CA and certificate for `127.0.0.1`. Copilot is configured to trust the CA for its exporter. [mkcert usage](https://github.com/FiloSottile/mkcert#usage)

```sh
relay_tls_dir="$HOME/.config/llm-meter/relay/local-tls"
(
  umask 077
  mkdir -p "$relay_tls_dir"
  CAROOT="$relay_tls_dir" mkcert \
    -cert-file "$relay_tls_dir/loopback.pem" \
    -key-file "$relay_tls_dir/loopback-key.pem" 127.0.0.1
)
```

In the macOS app, enter the full paths to `loopback.pem` and `loopback-key.pem` under **Settings → Local HTTPS**, then save. For the CLI, start the HTTPS listener with the same files.

```sh
./llm-meter-relay \
  --proxy=false \
  --telemetry-listen=127.0.0.1:4318 \
  --target https://proxy.example.com \
  --key-file "$HOME/.config/llm-meter/relay/proxy.key" \
  --data-dir "$HOME/.config/llm-meter/relay/copilot-data" \
  --tls-cert "$relay_tls_dir/loopback.pem" \
  --tls-key "$relay_tls_dir/loopback-key.pem"
```

With the relay running, start Copilot CLI in another terminal.

```sh
COPILOT_OTEL_ENABLED=true \
COPILOT_OTEL_EXPORTER_TYPE=otlp-http \
OTEL_EXPORTER_OTLP_ENDPOINT=https://127.0.0.1:4318/otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json \
OTEL_EXPORTER_OTLP_CERTIFICATE="$HOME/.config/llm-meter/relay/local-tls/rootCA.pem" \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT \
OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false \
copilot
```

If port `4318` is already in use, choose another loopback port and use it in the exporter URL. To share the HTTPS receiver, use its `https://` address and configure the CA trust in each exporter. LLM Meter uses Copilot's individual model-call spans for usage. [Copilot OpenTelemetry reference](https://docs.github.com/en/copilot/reference/enterprise-administrators/opentelemetry-reference)

#### OpenCode

With OpenCode v2.3.2 or a version with the same usage spans, launch a standalone session so the exporter settings apply to the server that runs the session.

Direct to the LLM Meter server (replace `https://proxy.example.com/otlp` with `http://localhost:8080/otlp` for a local server):

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=https://proxy.example.com/otlp \
OTEL_EXPORTER_OTLP_HEADERS="X-LLM-Meter-Key=<your-proxy-key>" \
opencode --standalone
```

Or via the local telemetry relay:

```sh
(
  unset OTEL_EXPORTER_OTLP_HEADERS
  OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318/otlp opencode --standalone
)
```

OpenCode V2 reads the endpoint from the environment and sends its logs and traces to LLM Meter. LLM Meter meters model usage from `SessionStep.attempt` spans. [OpenCode CLI reference](https://opencode.ai/docs/cli/#background-service), [OpenCode OTLP implementation](https://github.com/anomalyco/opencode/blob/v2.3.2/packages/util/src/observability/otlp.ts)

---

## 6. Security, Networking, Permissions, and Durable Queue Behavior

### 6.1 Docker loopback binding, private networking, and request guard

- **Bind published ports to loopback only:** Always publish `"127.0.0.1:8765:8765"` (never `"8765:8765"`). The relay itself injects your proxy key and does not require a proxy key from local clients, so exposing it on non-loopback interfaces would allow other hosts to attribute requests to your client.
- **Use Docker Engine 28 or newer** and keep the relay on a dedicated private Compose network (`networks: { relay: {} }`). Attach only trusted clients to that network, because containers sharing the network can reach the relay directly.
- **`--listen` vs. `--bind` in Docker:** Inside a container, `--bind=0.0.0.0:8765` listens on the container socket so Docker port forwarding works, while `--listen` (default `127.0.0.1:8765`) specifies the host-facing loopback address checked against each request's `Host` header. If you map a different host port such as `127.0.0.1:9000:8765`, pass `--listen=127.0.0.1:9000 --bind=0.0.0.0:8765`.
- **Built-in relay request guard:** The relay rejects:
  - Any `Host` header that does not match `--listen` (`403 Forbidden`).
  - Any request containing `Origin` or `Sec-Fetch-Site` headers (`403 Forbidden`, mitigating cross-site browser requests to loopback).
  - HTTP `CONNECT` requests, unknown routes, and path traversal segments (`.`, `..`, `\`) (`404 Not Found`).
  - Proxy requests missing upstream provider authorization (`401 Unauthorized`).

### 6.2 Key file permissions and container user mapping

The relay enforces strict checks when opening `--key-file`:

- Must exist and be a **regular file** (not a directory or pipe).
- On Linux and macOS, group and world permission bits must all be zero (`mode & 0077 == 0`, e.g., `0600` or `0400`).
- Must contain a single non-empty key of at most 4,096 bytes with printable ASCII characters and no internal whitespace (a trailing newline is allowed and trimmed).

**Container UID/GID choices:**

- **Recommended (default Compose example):** Run with `user: "${RELAY_UID}:${RELAY_GID}"` matching the host owner of `proxy.key` and `data`, and set `bind: { create_host_path: false }` so Docker fails fast instead of silently creating an empty directory if a path is missing.
- **Alternative (built-in image user):** Omit `user:` to run as the image's default `llmmeter` user (`10001:10001`), and `chown 10001:10001` on `proxy.key` (`0600`) and `data` (`0700`) on the host.

### 6.3 When `/data` is needed

| Component / mode | Is persistent `/data` needed? | What is stored there |
| --- | --- | --- |
| **LLM Meter server** | **Always** (`DATA_DIR=/data` in Docker, `./data` natively) | `llm-meter.db` — clients, hashed proxy keys, request/usage ledger, allowances, and cached pricing catalogs. |
| **Relay (Remote proxy alone)** | **No** | Mounting `/data` in the default Compose file makes it available when Usage-only or OpenTelemetry is enabled. |
| **Relay (Usage-only or OpenTelemetry)** | **Yes** (`--data-dir`) | `usage-outbox.db` stores pending events and the destination `(endpoint, key)` fingerprint. |

> **One `--data-dir` per `(server, key)` connection:** On first open, `usage-outbox.db` binds itself to a SHA-256 fingerprint of the upload endpoint (`https://<target>/api/usage/events`) and the proxy key. Starting a relay with a different `--target` or proxy key against the same directory fails immediately (`use a separate usage queue directory for a different server or key`) so pending events are never uploaded under the wrong client or server.

### 6.4 Durable queue, retry behavior, and graceful shutdown

- The relay queues metered HTTP requests, WebSocket usage, and OpenTelemetry usage reports in `usage-outbox.db`. Each event has a stable ID and a 16 KiB size limit.
- **Queue capacity:** Up to **10,000** pending events. When the queue fills, Usage-only drops new reports with log message `Could not queue a usage report`. The OpenTelemetry listener returns `503` so the exporter can retry the batch. Model requests can continue.
- **Ordered delivery & deduplication:** A single background worker uploads events strictly in FIFO order via `POST https://<target>/api/usage/events` with `X-LLM-Meter-Key` and a 10-second timeout. The server returns `204 No Content` and deduplicates retries atomically by `(client_id, event_id)`, so retries never double-count.
- The uploader sends requests to the configured HTTPS URL. A redirect response leaves the event queued for retry at that URL.
- **Retry backoff vs. permanent rejection:**
  - **Retryable errors** (network failure, timeout, `401`, `404`, `405`, `429`, `5xx`, redirects): event stays queued; relay logs `Usage event remains queued` and backs off exponentially from **1 second** up to **60 seconds** (`1s → 2s → 4s → ... → 60s`), resetting to 1s after a successful upload.
  - **Permanent rejections** (`400 Bad Request`, `413 Payload Too Large`, `422 Unprocessable Entity`): the rejected event is deleted so it cannot block subsequent events; relay logs `Discarded usage event rejected by the server`.
- On `SIGINT` or `SIGTERM`, the relay stops uploading, cancels open WebSocket connections, and gives HTTP and WebSocket handlers up to 10 seconds to finish recording usage. Those reports can upload on the next start. Set `stop_grace_period: 15s` in Docker Compose to give the relay time to shut down.

---

## 7. Lifecycle, Upgrades, and Key Rotation

### Migrating from the Codex and OpenCode Go proxy routes

The dedicated `/codex` and `/opencode-go` proxy routes and their `CODEX_UPSTREAM` and `OPENCODE_GO_UPSTREAM` settings have been removed. Use [OpenTelemetry](#53-opentelemetry) for Codex and OpenCode.

1. Pause new requests on the old proxy setup. If you used a usage-only relay, [drain its pending reports](#check-for-pending-reports) while the old server still runs.
2. Restore your native provider endpoint and keep your provider sign-in. For Codex, restore your original `model_provider` selection. In OpenCode, remove the meter-specific `settings.baseURL` and `X-LLM-Meter-Key` header overrides. Remove any LLM Meter header overrides in Codex as well.
3. Upgrade the LLM Meter server, then configure OpenTelemetry export:
   - **Direct to `/otlp`:** Point the harness's OpenTelemetry exporter at `https://proxy.example.com/otlp` (or `http://localhost:8080/otlp`) with `X-LLM-Meter-Key`, and retire any `--provider=codex` or `--provider=opencode-go` relay instance.
   - **Local telemetry relay:** Upgrade the relay, replace any `--provider=codex` or `--provider=opencode-go` instance with a telemetry relay, and point only the harness's OpenTelemetry exporter at the relay. Use a separate loopback port if another proxy relay is already running there.

The switch drops live Codex quota and reset tracking and OpenCode Go billed charges and allowance usage. The existing database keeps previously recorded usage and provider-reported values.

### Upgrading the LLM Meter server

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

### Upgrading the relay

```sh
export RELAY_UID="$(id -u)" RELAY_GID="$(id -g)"
docker compose pull relay
docker compose up -d relay
```

For native binaries, rebuild with `go build -o llm-meter-relay ./cmd/llm-meter-relay` and restart the process. Keeping the same `--data-dir` preserves queued events across the restart.

### Check for pending reports

Pause new client requests and let the relay upload the backlog. If you have the `sqlite3` CLI, check the number of pending reports using the path to your relay state directory. The following command uses the default Compose setup in this guide.

```sh
sqlite3 -readonly "$HOME/.config/llm-meter/relay/data/usage-outbox.db" 'SELECT COUNT(*) FROM events;'
```

A result of `0` means the queue is empty. If it stays above zero, check the relay logs for upload errors before rotating the key or changing modes.

### Switching relay modes

- **`remote-proxy` → `usage-only`:**
  1. Set `--data-dir` to a persistent directory.
  2. Add `--mode=usage-only` and restart the relay.
- **`usage-only` → `remote-proxy`:**
  1. [Check for pending reports](#check-for-pending-reports) and wait for the queue to empty.
  2. Remove `--mode=usage-only` and restart the relay.

### Rotating proxy keys or moving to another server

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
| `401 A valid X-LLM-Meter-Key is required` on proxy or direct `/otlp` requests | Sent `DASHBOARD_TOKEN`, missing/duplicate `X-LLM-Meter-Key` header, or revoked key | Create a client key (`llmm_...`) in **Clients** and send exactly one `X-LLM-Meter-Key` header. |

### Relay startup errors

| Error message | Likely cause | Fix |
| --- | --- | --- |
| `--target must be an HTTPS LLM Meter origin without a route, user info, query, or fragment` | The target contains a path, credentials, a query, or a fragment, or uses HTTP | Set `--target` to a server origin such as `https://proxy.example.com`. |
| `--upstream requires --mode usage-only` | Passed `--upstream` in `remote-proxy` mode | Add `--mode usage-only`, or set `*_UPSTREAM` on the LLM Meter server instead. |
| `--upstream requires --provider` | Passed `--upstream` without a provider selection | Set `--provider` to the provider whose base URL you want to override. |
| `the provider and LLM Meter must use different hosts in usage-only mode` | `--upstream` (or default provider upstream) shares a hostname with `--target` | Point `--target` at your LLM Meter server host and `--upstream` at the provider host. |
| `--listen must specify a loopback IP and port` / `--bind requires 0.0.0.0 and a fixed port...` | Non-loopback `--listen`, ephemeral port `0`, or non-`0.0.0.0` `--bind` | Use `127.0.0.1:<port>` or `[::1]:<port>` for `--listen`; in Docker pair it with `--bind=0.0.0.0:<port>`. |
| `cannot open the proxy key file` / `must be a regular file` / `must be accessible only to its owner` / `must contain a single key` | Wrong path, directory mounted instead of file, loose permissions, container UID mismatch, or extra lines/whitespace | Verify `proxy.key` exists as a regular single-line file, run `chmod 600 proxy.key`, and match `RELAY_UID`/`RELAY_GID` (or `10001:10001`). |
| `use a separate usage queue directory for a different server or key` | Reused a `--data-dir` already bound to another `--target` or proxy key | Drain pending events with the old settings if needed, then use a fresh empty `--data-dir`. |

### Client requests through the relay

| HTTP status / Response | Likely cause | Fix |
| --- | --- | --- |
| `403 Forbidden` | Request `Host` header does not match `--listen` (e.g., client used `localhost:8765` instead of `127.0.0.1:8765`, or host port mapping differs from `--listen`), or request included `Origin` / `Sec-Fetch-Site` | Point clients at `http://127.0.0.1:<port>` matching `--listen`, and do not call the relay from browser contexts sending `Origin`/`Sec-Fetch-Site`. |
| `401 Google authorization is required` / `401 Provider authorization is required` | Client did not send its upstream auth header | Confirm Antigravity is signed in to Google or the OpenAI-compatible client has its provider API key. |
| `404 Not Found` | Wrong client base URL or route excluded by `--provider` | Check the client base URL and provider scope. [Client base URLs](#proxy-client-base-paths) |
| `502 LLM Meter unavailable` (`remote-proxy`) | Relay cannot connect to the LLM Meter server over HTTPS | Check DNS, TLS certificate trust, firewall/VPN, and server `/healthz`. |
| `502 Upstream unavailable` (`usage-only` or direct server) | Provider upstream is unreachable or refused the connection | Check outbound HTTPS connectivity and any `--upstream` / `*_UPSTREAM` override. |

### Usage uploads, pricing, and correlation

| Symptom / Log message | Likely cause | Fix |
| --- | --- | --- |
| `Usage event remains queued` (`HTTP 404` or `HTTP 405`) | Usage endpoint unavailable at the configured target | Verify the target origin and that its ingress routes `POST /api/usage/events` to LLM Meter. |
| `Usage event remains queued` (`HTTP 401`) | Proxy key in `proxy.key` is invalid or revoked | Verify the key matches an active client key in **Clients** (follow [key rotation](#rotating-proxy-keys-or-moving-to-another-server) if replacing it). |
| `Usage event remains queued` (`HTTP 301`/`302`/`307`/`308`) | Reverse proxy is redirecting `POST /api/usage/events` | Point `--target` at the canonical HTTPS hostname; the relay intentionally does not follow redirects. |
| `Could not queue a usage report` | Queue hit the 10,000-event cap during an outage, or `--data-dir` is unwritable/full | Restore connectivity to the server so the queue drains; verify `--data-dir` ownership (`RELAY_UID`/`RELAY_GID`) and free disk space. |
| `Discarded usage event rejected by the server` (`400`/`413`/`422`) | Server rejected the payload (version mismatch or host clock more than 10 minutes ahead of server) | Synchronize system clocks (NTP) and keep relay and server versions aligned. |
| Requests appear as **Unpriced** | Provider omitted cost and model/category lacks a rate, or Models.dev flags separate audio rates | Check server logs for pricing feed refresh errors or add complete rates for the model in `PRICING_FILE`. |
| Background response completion tokens missing on original request | Creation (`POST /v1/responses`) and polling (`GET /v1/responses/<id>`) used different dashboard clients or mixed `proxy` and `relay` modes | Route creation and polling through the same dashboard client and mode. |
