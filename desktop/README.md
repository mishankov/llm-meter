# LLM Meter Relay for macOS

Run a local LLM Meter relay from the macOS menu bar to share one Keychain-stored client key across all local tools, strip non-usage telemetry locally, queue uploads offline in SQLite, and support endpoint-only proxy clients. In **Settings**, enter your LLM Meter HTTPS origin and a dashboard client key. Under **Local listeners**, enable **Model proxy**, **OpenTelemetry**, or both. Save with ⌘S and start the relay if it is stopped.

- **Model proxy** serves generic OpenAI-compatible clients and Antigravity on port `8765` by default. Choose **Usage-only** to proxy requests directly to providers or **Remote proxy** to route requests through your server.
- **OpenTelemetry** receives usage from Claude Code, Codex, Gemini CLI, GitHub Copilot CLI, and OpenCode on port `4318` by default. For GitHub Copilot CLI, set the certificate and private key paths under **Local HTTPS**. [OpenTelemetry setup](../docs/configuration.md#53-opentelemetry)

Use **Client Setup** for both listeners' URLs. Press ⌘⇧E to copy the OpenTelemetry endpoint or ⌘⇧C to copy the proxy snippet.

The app stores the proxy key in Keychain and keeps settings and usage queues in `~/Library/Application Support/LLM Meter Relay/`. Each server and key pair has its own queue. When switching with pending reports, you can drain the old queue or keep it for later. Choose **Upload kept reports** in **Status** to send retained reports to their original servers, including after restarting the app. The app keeps each original credential in Keychain until its queues finish uploading and the active profile no longer needs it.

## Build and run

On macOS, install Xcode Command Line Tools, Go 1.26 or later, and Node.js 24. From this directory, run

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
export PATH="$(go env GOPATH)/bin:$PATH"
wails3 task package
open "bin/LLM Meter Relay.app"
```

The build produces an ad-hoc signed app for the current Mac architecture. To create a disk image, run `wails3 task package:dmg`. Move the app to its permanent location before enabling **Launch at Login**.

## Development

Run `wails3 task dev` for hot reload. After changing a bound Go method or type, regenerate its checked-in TypeScript bindings with `wails3 task bindings`. To verify the desktop module, build the frontend first, then run `go test -race ./...` here. The CLI and server use the repository's root Go module.

Run the frontend checks with `npm run check --prefix frontend`. For the keyboard flows, run `npx playwright install webkit` from `frontend/`, then `npm test` in that directory.
