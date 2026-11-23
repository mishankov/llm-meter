<script lang="ts">
  import CopyIcon from "@lucide/svelte/icons/copy";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import { Button } from "@ui-sdk/svelte/button";
  import { bridge, errorMessage, formatTime, type AppSnapshot } from "./bridge";

  let {
    snapshot,
    busy,
    onOpenSettings,
    onDrainStop,
    onUploadKept,
    onUpdate,
  }: {
    snapshot: AppSnapshot;
    busy: boolean;
    onOpenSettings: () => void;
    onDrainStop: () => void;
    onUploadKept: () => void;
    onUpdate: (snap: AppSnapshot) => void;
  } = $props();

  let copiedUrl = $state("");
  let feedback = $state("");
  let tested = $state("");
  let testing = $state(false);
  let retrying = $state(false);

  const running = $derived(snapshot.status.state === "running");
  const draining = $derived(snapshot.status.state === "draining");
  const outbox = $derived(snapshot.status.outbox ?? null);
  const pendingCount = $derived(outbox ? outbox.pending : snapshot.pendingOnDisk);
  const endpoints = $derived(snapshot.endpoints ?? []);
  const routes = $derived(snapshot.status.routes ?? []);

  async function copyEndpoint(url: string) {
    try {
      await bridge.copyText(url);
    } catch (err) {
      feedback = errorMessage(err);
      return;
    }
    copiedUrl = url;
    setTimeout(() => {
      if (copiedUrl === url) copiedUrl = "";
    }, 1800);
  }

  async function retryNow() {
    retrying = true;
    feedback = "";
    try {
      onUpdate(await bridge.retryUploads());
    } catch (err) {
      feedback = errorMessage(err);
    } finally {
      retrying = false;
    }
  }

  async function testServer() {
    testing = true;
    feedback = "";
    tested = "";
    try {
      await bridge.testConnection(snapshot.config.target);
      tested = "Server responded on /healthz.";
    } catch (err) {
      feedback = errorMessage(err);
    } finally {
      testing = false;
    }
  }

  async function openDashboard() {
    feedback = "";
    try {
      await bridge.openDashboard();
    } catch (err) {
      feedback = errorMessage(err);
    }
  }
</script>

<div class="stack">
  {#if snapshot.keptQueues > 0}
    <section class="card">
      <div class="card-head">
        <h2>Reports from previous settings</h2>
        <Button class="secondary-button compact-button" title="Upload kept reports (⌘⇧P)" aria-keyshortcuts="Meta+Shift+P" disabled={busy || draining} onclick={onUploadKept}>
          Upload kept reports
        </Button>
      </div>
      <p class="muted">Send pending reports to their original server using the saved key.</p>
    </section>
  {/if}
  {#if !snapshot.configured}
    <section class="card onboarding">
      <div class="onboarding-copy">
        <h2>Connect to your LLM Meter server</h2>
        <p class="muted">
          Enter your HTTPS server origin and a dashboard client proxy key to start metering local Codex, OpenCode,
          Antigravity, and OpenAI-compatible traffic.
        </p>
      </div>
      <Button class="primary-button" onclick={onOpenSettings}>
        <SettingsIcon size={14} aria-hidden="true" />
        Configure relay
      </Button>
    </section>
  {:else}
    {#if snapshot.status.lastError}
      <div class="banner banner-error" role="alert">
        <span>{snapshot.status.lastError}</span>
      </div>
    {/if}

    {#if feedback}
      <div class="banner banner-error" role="alert">
        <span>{feedback}</span>
      </div>
    {/if}

    <section class="card">
      <div class="card-head">
        <h2>Relay Connection</h2>
        <div class="inline-actions">
          <Button class="secondary-button compact-button" title="Test server (⌘⇧T)" aria-keyshortcuts="Meta+Shift+T" disabled={testing} onclick={testServer}>
            {testing ? "Testing…" : "Test Server"}
          </Button>
          <Button class="secondary-button compact-button" onclick={openDashboard}>
            <ExternalLinkIcon size={13} aria-hidden="true" />
            Open Dashboard
          </Button>
        </div>
      </div>

      {#if tested}<p class="banner banner-success" role="status">{tested}</p>{/if}
      <dl class="metrics">
        <div>
          <dt>State</dt>
          <dd class="state-val">
            <span class="dot" data-state={snapshot.status.state} aria-hidden="true"></span>
            {snapshot.status.state}
          </dd>
        </div>
        <div>
          <dt>Mode</dt>
          <dd>{snapshot.status.mode === "usage-only" ? "Usage-only (direct to providers)" : "Remote proxy"}</dd>
        </div>
        <div>
          <dt>Local listener</dt>
          <dd class="mono numeric">http://{snapshot.status.listenAddr || snapshot.config.listen}</dd>
        </div>
        <div>
          <dt>LLM Meter server</dt>
          <dd class="mono">{snapshot.config.target}</dd>
        </div>
        <div>
          <dt>Proxy key</dt>
          <dd class="mono">{snapshot.keyPrefix || "Configured"}</dd>
        </div>
        <div>
          <dt>Requests handled</dt>
          <dd class="numeric">{snapshot.status.requestsTotal}</dd>
        </div>
      </dl>

      <div class="routes-row">
        <span class="muted label-sm">Active routes</span>
        <div class="chips">
          {#each routes as route (route)}
            <span class="chip">{route}</span>
          {/each}
        </div>
      </div>
    </section>

    {#if snapshot.config.mode === "usage-only" || pendingCount > 0}
      <section class="card">
        <div class="card-head">
          <h2>Usage Outbox</h2>
          <div class="inline-actions">
            {#if running && pendingCount > 0}
              <Button class="secondary-button compact-button" title="Retry pending uploads (⌘⇧U)" aria-keyshortcuts="Meta+Shift+U" disabled={retrying || busy} onclick={retryNow}>
                <RefreshCwIcon size={13} aria-hidden="true" />
                Retry Now
              </Button>
              <Button class="secondary-button compact-button" title="Drain and stop (⌘⇧Return)" aria-keyshortcuts="Meta+Shift+Enter" disabled={busy || draining} onclick={onDrainStop}>
                Drain &amp; Stop
              </Button>
            {/if}
          </div>
        </div>

        {#if outbox?.lastError}
          <div class="banner banner-warning" role="status">
            <span>{outbox.lastError}</span>
            {#if outbox.nextRetryAt && !outbox.nextRetryAt.startsWith("0001-01-01")}
              <span class="numeric">Next retry {formatTime(outbox.nextRetryAt)}</span>
            {/if}
          </div>
        {/if}

        <dl class="metrics outbox-metrics">
          <div>
            <dt>Pending events</dt>
            <dd class="numeric" class:warn-text={pendingCount > 0}>{pendingCount}</dd>
          </div>
          <div>
            <dt>Last upload</dt>
            <dd class="numeric">{formatTime(outbox?.lastSuccessAt)}</dd>
          </div>
          <div>
            <dt>Dropped</dt>
            <dd class="numeric">{outbox?.dropped ?? 0}</dd>
          </div>
        </dl>
      </section>
    {/if}

    <section class="card">
      <div class="card-head">
        <h2>Local Client URLs</h2>
        <span class="muted label-sm">No X-LLM-Meter-Key header needed</span>
      </div>
      <div class="endpoint-list">
        {#each endpoints as ep (ep.provider)}
          <div class="endpoint-row">
            <div class="endpoint-info">
              <span class="endpoint-label">{ep.label}</span>
              <code class="endpoint-url">{ep.url}</code>
            </div>
            <Button
              class="secondary-button compact-button"
              onclick={() => copyEndpoint(ep.url)}
              aria-label={`Copy ${ep.label} URL`}
            >
              <CopyIcon size={12} aria-hidden="true" />
              {copiedUrl === ep.url ? "Copied" : "Copy"}
            </Button>
          </div>
        {/each}
      </div>
    </section>
  {/if}
</div>

<style>
  .stack {
    display: grid;
    gap: 14px;
  }

  .onboarding {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 20px;
  }

  .onboarding h2 {
    font-size: 15px;
    margin-bottom: 6px;
  }

  .onboarding p {
    margin: 0;
    line-height: 1.5;
    max-width: 440px;
  }

  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 12px;
  }

  .card-head h2 {
    font-size: 13.5px;
    font-weight: 620;
    margin: 0;
  }

  .inline-actions {
    display: flex;
    gap: 6px;
  }

  .metrics {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px 18px;
    margin: 0;
  }

  .outbox-metrics {
    grid-template-columns: repeat(3, minmax(0, 1fr));
    margin-top: 10px;
  }

  dt {
    color: var(--muted);
    font-size: 11px;
    margin-bottom: 2px;
  }

  dd {
    margin: 0;
    font-size: 12.5px;
    font-weight: 520;
    overflow-wrap: anywhere;
  }

  .mono {
    font-family: var(--mono);
    font-size: 12px;
  }

  .state-val {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    text-transform: capitalize;
  }

  .dot {
    inline-size: 7px;
    block-size: 7px;
    border-radius: 50%;
    background: var(--muted);
  }

  .dot[data-state="running"] {
    background: var(--success);
  }

  .dot[data-state="draining"] {
    background: var(--warning);
  }

  .dot[data-state="error"] {
    background: var(--danger);
  }

  .warn-text {
    color: var(--warning);
  }

  .routes-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-top: 12px;
    padding-top: 10px;
    border-top: 1px solid var(--line);
  }

  .label-sm {
    font-size: 11.5px;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
  }

  .chip {
    font-family: var(--mono);
    font-size: 11px;
    padding: 2px 7px;
    border-radius: 4px;
    background: var(--surface-raised);
    border: 1px solid var(--line);
    color: var(--nav-current-text);
  }

  .endpoint-list {
    display: grid;
    gap: 8px;
  }

  .endpoint-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 8px 10px;
    border-radius: 6px;
    background: var(--bg);
    border: 1px solid var(--line);
  }

  .endpoint-info {
    display: grid;
    gap: 2px;
    min-width: 0;
  }

  .endpoint-label {
    font-size: 11.5px;
    color: var(--muted);
  }

  .endpoint-url {
    font-size: 12px;
    overflow-wrap: anywhere;
  }
</style>
