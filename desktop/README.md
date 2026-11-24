# LLM Meter Relay for macOS

Run one local relay from the menu bar. In **Settings**, enter your LLM Meter HTTPS origin and a dashboard client key. Choose **usage-only** to send usage reports while clients connect directly to providers, or **remote-proxy** to forward model requests through your server. Copy the matching loopback URL or configuration for generic OpenAI-compatible clients or Antigravity from **Client Setup**. For Claude Code, Codex, Gemini CLI, GitHub Copilot CLI, and OpenCode, run the relay CLI in `telemetry` mode. [OpenTelemetry setup](../docs/configuration.md#53-opentelemetry)

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
