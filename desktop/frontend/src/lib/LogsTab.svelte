<script lang="ts">
  import Trash2Icon from "@lucide/svelte/icons/trash-2";
  import { Button } from "@ui-sdk/svelte/button";
  import { bridge, errorMessage, formatTime, type AppSnapshot } from "./bridge";

  let {
    snapshot,
    onUpdate,
  }: {
    snapshot: AppSnapshot;
    onUpdate: (snap: AppSnapshot) => void;
  } = $props();

  const entries = $derived([...(snapshot.logs ?? [])].reverse());

  let clearing = $state(false);
  let clearError = $state("");
  async function clear() {
    clearing = true;
    clearError = "";
    try {
      onUpdate(await bridge.clearLogs());
    } catch (err) {
      clearError = errorMessage(err);
    } finally {
      clearing = false;
    }
  }
</script>

<section class="card logs-card">
  <div class="logs-head">
    <div>
      <h2>Relay Logs</h2>
      <p class="muted subtitle">Sanitized operational events (proxy keys and provider tokens are never logged).</p>
    </div>
    <Button class="secondary-button compact-button" disabled={entries.length === 0 || clearing} title="Clear logs (⌘K)" aria-keyshortcuts="Meta+K" onclick={clear}>
      <Trash2Icon size={12} aria-hidden="true" />
      Clear
    </Button>
  </div>

  {#if clearError}<p class="banner banner-error" role="alert">{clearError}</p>{/if}
  {#if entries.length === 0}
    <div class="empty muted">No log entries yet.</div>
  {:else}
    <div class="log-list" role="log" aria-live="polite">
      {#each entries as entry, index (`${entry.timestamp}-${index}`)}
        <div class="log-row">
          <span class="log-time numeric muted">{formatTime(entry.timestamp)}</span>
          <span class="log-level" data-level={entry.level}>{entry.level}</span>
          <span class="log-msg">{entry.message}</span>
        </div>
      {/each}
    </div>
  {/if}
</section>

<style>
  .logs-card {
    display: grid;
    gap: 12px;
  }

  .logs-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  h2 {
    font-size: 13.5px;
    font-weight: 620;
    margin: 0 0 3px;
  }

  .subtitle {
    margin: 0;
    font-size: 11.5px;
  }

  .empty {
    padding: 28px 12px;
    text-align: center;
    font-size: 12px;
    border-radius: 6px;
    background: var(--bg);
    border: 1px solid var(--line);
  }

  .log-list {
    display: grid;
    gap: 4px;
    max-height: 360px;
    overflow-y: auto;
    padding: 8px 10px;
    border-radius: 6px;
    background: var(--bg);
    border: 1px solid var(--line);
    font-family: var(--mono);
    font-size: 11.5px;
  }

  .log-row {
    display: grid;
    grid-template-columns: max-content 38px minmax(0, 1fr);
    gap: 8px;
    align-items: baseline;
    line-height: 1.45;
  }

  .log-level {
    text-transform: uppercase;
    font-size: 10px;
    font-weight: 640;
    color: var(--muted);
  }

  .log-level[data-level="warn"] {
    color: var(--warning);
  }

  .log-level[data-level="error"] {
    color: var(--danger);
  }

  .log-msg {
    overflow-wrap: anywhere;
  }
</style>
