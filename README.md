# LLM Proxy

A Go reverse proxy with a SvelteKit dashboard for tracking token usage across OpenAI-compatible clients and signed-in Codex sessions. The dashboard charts estimated API costs, values Codex subscription usage at API rates, and attributes requests to clients by their proxy keys.

## Run with Docker

```sh
export DASHBOARD_TOKEN="$(openssl rand -hex 32)"
docker build -t llm-proxy .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e DASHBOARD_TOKEN -e COOKIE_SECURE=false \
  -v llm-proxy-data:/data llm-proxy
```

Open `http://localhost:8080` and enter the value of `DASHBOARD_TOKEN`. Go serves the bundled dashboard and proxy from the same container. The volume holds the SQLite database and survives container replacement. Keep the dashboard token stable across restarts. For remote access on a VPS or Pi, serve the container through an HTTPS ingress and leave `COOKIE_SECURE` at its default `true`.

Both Linux `amd64` and `arm64` are supported. Build on the deployment machine, or select the platform with `docker buildx build --platform linux/arm64 --load -t llm-proxy .` for a Raspberry Pi.

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

## Usage and rates

The ledger stores request metadata, reported token counts, and the rates and USD estimate recorded at the time of use. The dashboard marks requests whose usage is unavailable or whose rate is unknown. Token prices estimate API usage and Codex's API-equivalent value. Provider-specific non-token fees and negotiated discounts fall outside that estimate.

Set `PRICING_FILE` to a JSON object of per-model overrides, in USD per million tokens. For example:

```json
{
  "my-model": { "input": 2, "cachedInput": 0.1, "output": 10, "cacheWrite": 2.5, "cacheWrite12h": 5 }
}
```

Other server settings are `DATA_DIR` (default `./data`, or `/data` in Docker), `LISTEN_ADDR` (default `:8080`), and the optional `OPENAI_UPSTREAM` and `CODEX_UPSTREAM` base URLs.

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
