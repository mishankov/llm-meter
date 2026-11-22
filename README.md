# LLM Meter

LLM Meter is a self-hosted dashboard for tracking tokens, costs, and provider allowances across OpenAI-compatible clients, Codex, OpenCode Go, and Antigravity. Meter requests on the server, or run a local relay that sends usage metadata while model traffic goes directly to the provider.

See the [Configuration Guide](docs/configuration.md) for choosing between direct, `remote-proxy`, and `usage-only` relay setups, complete server/relay options, client examples, durable queue behavior, upgrades, and troubleshooting.

## Run with Docker

```sh
export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
docker build -t llm-meter .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e DASHBOARD_TOKEN -e COOKIE_SECURE=false \
  -v llm-proxy-data:/data llm-meter
```

Open `http://localhost:8080` and enter the value of `DASHBOARD_TOKEN`. Go serves the bundled dashboard and proxy from the same container. The volume holds the SQLite database and survives container replacement. Keep the dashboard token stable across restarts. For remote access on a VPS or Pi, serve the container through an HTTPS ingress and leave `COOKIE_SECURE` at its default `true`.

CI publishes a multi-platform image for Linux `amd64` and `arm64` at `ghcr.io/mishankov/llm-meter`. Use `:latest` for the default branch, `:branch-<branch-name>` for a branch build (slashes become hyphens), `:sha-<full-commit-sha>` to pin a commit, or a release tag such as `:v1.2.3`. To use a published build, pull the desired tag and substitute the full image name for `llm-meter` in the `docker run` command.

Upgrading from LLM Proxy? CI also publishes the existing `llm-proxy` and `llm-proxy-relay` image names. Keep your current client settings, keys, and data mounts when changing the image tag. [Upgrade instructions](docs/configuration.md#upgrading-from-llm-proxy)

## Connect a client

1. Open **Clients**, create a client, and copy its proxy key when shown.
2. Set the client's OpenAI base URL to `https://proxy.example.com/v1` (or `http://localhost:8080/v1` for local use).
3. Send the proxy key in `X-LLM-Proxy-Key` alongside the client's usual upstream `Authorization` header. The proxy validates its own key and strips it before forwarding the request.

For OpenCode, add these fields to the OpenAI provider in `opencode.jsonc` and set `LLM_PROXY_KEY` in its environment:

```jsonc
{
  "providers": {
    "openai": {
      "settings": { "baseURL": "https://proxy.example.com/v1" },
      "headers": { "X-LLM-Proxy-Key": "{env:LLM_PROXY_KEY}" }
    }
  }
}
```

If OpenCode's `openai` provider is signed in with ChatGPT, use `https://proxy.example.com/backend-api/codex` as its base URL. For the separate `opencode-go` provider, set its `settings.baseURL` to `https://proxy.example.com/opencode-go` and add the same proxy header. OpenCode keeps using its saved upstream credentials for each provider.

### Signed-in Codex

Create a separate client key for Codex. In the user `config.toml` read by the Codex process that T3 uses, select a provider that keeps Codex's existing sign-in and sends the proxy access header. Use a new session after changing the provider. Supply `LLM_PROXY_KEY` through the environment of that Codex process.

```toml
model_provider = "metered-codex"

[model_providers.metered-codex]
name = "OpenAI"
base_url = "https://proxy.example.com/backend-api/codex"
wire_api = "responses"
requires_openai_auth = true
supports_websockets = true
env_http_headers = { "X-LLM-Proxy-Key" = "LLM_PROXY_KEY" }
```

The Codex route forwards to the ChatGPT Codex backend. The ordinary `/v1` route forwards to the OpenAI API. The dashboard shows remaining Codex allowance when the upstream reports a recent reading.

### Signed-in Antigravity

Create an Antigravity client in the dashboard and use `https://proxy.example.com/antigravity` as the Cloud Code Assist base URL. The proxy appends the native request path, such as `/v1internal:loadCodeAssist` or `/v1internal:streamGenerateContent`, to `ANTIGRAVITY_UPSTREAM` (default `https://daily-cloudcode-pa.googleapis.com`).

Keep Antigravity signed in to Google. Requests must include Google's original `Authorization` header and `X-LLM-Proxy-Key` from your local secret store. Use HTTPS for the proxy and keep both credentials out of URLs, command arguments, and logs.

If your Antigravity client accepts only an endpoint override, [run the relay](#run-the-relay) on the same machine as the ACP process.

For T3 Code, set **Settings → Providers → Antigravity → Binary path** to an executable launcher named `antigravity` or `agy` on the environment that runs Antigravity. The launcher adds the endpoint flag to ACP invocations and passes through T3's profile arguments and other commands:

```sh
#!/bin/sh
if [ "$1" = acp ]; then
  shift
  set -- acp --cloud_code_endpoint http://127.0.0.1:8765 "$@"
fi
exec "/absolute/path/to/antigravity" "$@"
```

Set the last line to your installed Antigravity executable, make the launcher executable with `chmod +x`, and start a fresh Antigravity session in T3 using your existing Google sign-in. You can confirm flag support on your runtime with `antigravity acp --help`.

The dashboard shows requests as **Antigravity** under the client that owns the proxy key. Generation responses contribute token counts when Google reports them.

## Run the relay

One relay serves all four supported providers on the same port. Set `--target` to the LLM Meter server's HTTPS origin, such as `https://proxy.example.com`, and supply one proxy key file. Requests across all four routes are attributed to the dashboard client that owns the key and retain their individual provider labels.

| Mode | Model traffic | Usage |
| --- | --- | --- |
| `usage-only` | Relay connects to the provider directly | Relay uploads usage metadata to the server |
| `remote-proxy` (default) | Relay connects to LLM Meter, which forwards to the provider | Server meters the proxied request |

Usage-only mode sends the model, token counts, outcome, and any reported costs or quotas. Prompts, responses, and provider credentials travel between the client, relay, and provider. The server authenticates usage reports with the dashboard client key and trusts that client to report its usage. Pending reports survive restarts, and the server deduplicates retries. The relay logs dropped reports when the queue fills or the server rejects an invalid report. Keep background request creation and polling on the same dashboard client and mode so the server can correlate them. The request API identifies how each request was metered in `measurementSource` (`relay` or `proxy`). Use a server version that supports usage ingestion before starting a `usage-only` relay.

| Provider | Client base URL | HTTPS server path in `remote-proxy` mode |
| --- | --- | --- |
| `antigravity` | `http://127.0.0.1:8765` | `/antigravity` |
| `openai` (OpenAI-compatible) | `http://127.0.0.1:8765/v1` | `/v1` |
| `codex` | `http://127.0.0.1:8765/backend-api/codex` | `/backend-api/codex` |
| `opencode-go` | `http://127.0.0.1:8765/opencode-go` | `/opencode-go` |

Point each client at its local base URL and use its usual provider authentication. The relay picks the provider from the request path and uses the provider's standard upstream in `usage-only` mode.

To limit an instance to one route, set `--provider` to one of `antigravity`, `openai`, `codex`, or `opencode-go`. In `usage-only` mode, `--upstream` sets another trusted HTTPS provider base URL for that route. An upstream override without `--provider` keeps the original Antigravity scope. A `remote-proxy` target URL ending in one of the server paths in the table also limits the relay to that route, preserving existing commands such as `--target https://proxy.example.com/antigravity`.

### Relay with Docker

The relay image is `ghcr.io/mishankov/llm-meter-relay`, with the same Linux platforms and tag scheme as the server. Authenticate to GHCR if the package is private. You can also build it from the checkout with `docker build -f Dockerfile.relay -t llm-meter-relay .` and substitute that image name below.

```sh
relay_dir="$HOME/.config/llm-meter/relay"
(umask 077; mkdir -p "$relay_dir/data"; touch "$relay_dir/proxy.key")
chmod 600 "$relay_dir/proxy.key"
```

Save the dashboard client key as the only line in `proxy.key`. Save this as `compose.yaml` and replace `https://proxy.example.com` with your server's HTTPS origin.

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

```sh
export RELAY_UID="$(id -u)" RELAY_GID="$(id -g)"
docker compose up -d relay
```

Add `--mode=usage-only` under `command` to send only usage reports to a server that supports ingestion. Keep the state directory mounted so pending reports survive container replacement. Drain pending reports before moving to a different usage server or key, then use a fresh state directory.

Attach only trusted clients to the relay's Docker network because containers on that network can reach the relay directly. Use Docker Engine 28 or newer and keep the published port bound to `127.0.0.1`. [Docker bridge networking](https://docs.docker.com/engine/network/drivers/bridge/) · [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/#publishing-ports)

### Relay as a Go binary

Build with `go build -o llm-meter-relay ./cmd/llm-proxy-relay`. The binary listens on `127.0.0.1:8765` by default.

```sh
./llm-meter-relay --target https://proxy.example.com --key-file "$HOME/.config/llm-meter/relay/proxy.key"
```

For usage-only mode, add `--mode usage-only --data-dir "$HOME/.config/llm-meter/relay/data"`.

## Usage and rates

The ledger saves provider-reported amounts, including zero, and uses local rates when the provider omits the amount. OpenCode Go list value and allowance use are recorded separately from the reported charge. Request details show both when available.

The proxy refreshes the public OpenCode Go and Models.dev price catalogs at startup and every 24 hours. It stores the last usable catalogs in SQLite, so a failed refresh or temporary outage leaves rates available. Each priced request retains the amount and rates recorded at the time of use. Rows display **Unpriced** when a required price is unknown or Models.dev lists separate audio rates that cannot be applied to the total token counts.

Upgrading from an older catalog requires one successful Models.dev refresh to check for separate audio rates. The ledger also applies the published 2-hour and 8-hour discounts to eligible OpenAI background estimates when a completed response is retrieved.

Set `PRICING_FILE` to a JSON object of per-model overrides, in USD per million tokens. Overrides take precedence over downloaded and built-in rates when estimating a cost. Include a rate for each token category used by the model. Use `0` for a category that is free. For example:

```json
{
  "my-model": { "input": 2, "cachedInput": 0.1, "output": 10, "cacheWrite": 2.5, "cacheWrite12h": 5 }
}
```

Other server settings are `DATA_DIR` (default `./data`, or `/data` in Docker), `LISTEN_ADDR` (default `:8080`), and the optional `OPENAI_UPSTREAM`, `CODEX_UPSTREAM`, `OPENCODE_GO_UPSTREAM`, and `ANTIGRAVITY_UPSTREAM` base URLs.

## Develop

Use Go 1.25+ and Node.js 24. The static frontend goes into `web/build`. The container build embeds it in the binary.

```sh
npm ci --prefix web
npm run check --prefix web
npm run build --prefix web
go test -race ./...

export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
COOKIE_SECURE=false LISTEN_ADDR=127.0.0.1:8080 go run .
```

Run the dashboard keyboard and interaction tests with `npm exec --prefix web -- playwright install chromium` and `npm test --prefix web`.
