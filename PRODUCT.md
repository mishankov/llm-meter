# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

LLM Meter is for developers and homelab operators who track usage across their AI clients. They use the dashboard to see which clients and models are consuming tokens, estimate the cost, and manage access keys.

## Product Purpose

Give operators one place to inspect model usage across connected clients. An operator can see where tokens and estimated spend went, filter by client or model, and open individual requests to understand the totals.

## Positioning

LLM Meter tracks tokens and estimated spend across AI clients. Claude Code, Codex, Gemini CLI, GitHub Copilot CLI, and OpenCode send usage through OpenTelemetry to the local relay. Generic OpenAI-compatible clients and Antigravity can send model traffic through the server proxy or local relay. A client key attributes usage while the client keeps its upstream credentials.

## Operating Context

- Run the server locally or on a self-hosted host. The Go service serves the dashboard, proxy routes, and usage ingestion API and stores usage in SQLite.
- Unlock the dashboard with the dashboard token. Create a client in **Clients** and copy its proxy key. Send the key in `X-LLM-Meter-Key` for direct connections or supply it to the relay in a private key file.
- Use **Usage** to inspect estimates and token counts over time, compare clients and models, and drill into recent requests. Use **Clients** to issue and revoke keys.

## Capabilities and Constraints

- Dashboard estimates are shown in USD. The ledger preserves provider-reported amounts, including zero, and uses recorded rates for estimates where applicable. Requests with insufficient pricing data appear as **Unpriced**.
- Historical OpenCode Go records preserve reported charges, list values, and allowance use. Historical Codex quota observations remain available.
- The full proxy key is shown only at issuance. The proxy validates the key and removes its header before forwarding the request upstream.

## Product Principles

- Make usage attributable to a client and traceable to individual requests.
- Preserve the distinction between estimated usage, provider-reported charges, and allowance consumption.
- Keep client access manageable while preserving each client's upstream sign-in or credentials.

## Accessibility & Inclusion

Give primary commands discoverable keyboard shortcuts and expose shortcut metadata where applicable. Buttons and shortcuts share actions, respect disabled and busy states and active dialogs, and preserve unsaved drafts. Handle IME composition, key repeat, and non-English keyboard layouts. Verify primary keyboard flows with the dashboard's Playwright harness when changing the UI.
