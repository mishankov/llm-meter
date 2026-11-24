<script lang="ts">
  import ActivityIcon from "@lucide/svelte/icons/activity";
  import PlayIcon from "@lucide/svelte/icons/play";
  import ScrollTextIcon from "@lucide/svelte/icons/scroll-text";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import SquareIcon from "@lucide/svelte/icons/square";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import { Button } from "@ui-sdk/svelte/button";
  import { Tabs, TabsList, TabsPanel, TabsTab, TabsTabIcon } from "@ui-sdk/svelte/tabs";
  import { onMount } from "svelte";
  import appMark from "./assets/llm-meter.png";
  import {
    bridge,
    errorMessage,
    type AppSnapshot,
    type AutostartStatus,
  } from "./lib/bridge";
  import ClientSetupTab from "./lib/ClientSetupTab.svelte";
  import LogsTab from "./lib/LogsTab.svelte";
  import SettingsTab from "./lib/SettingsTab.svelte";
  import { activateShortcut, handleShortcut } from "./lib/shortcuts";
  import StatusTab from "./lib/StatusTab.svelte";

  let snapshot = $state<AppSnapshot | null>(null);
  let autostart = $state<AutostartStatus>({ enabled: false, available: false });
  let activeTab = $state<string | null>(location.hash === "#settings" ? "settings" : "status");
  let busy = $state(false);
  let saving = $state(false);
  let actionConfirmation = $state("");
  let actionError = $state("");

  onMount(() => {
    let active = true;
    const openSettings = () => activateShortcut("Meta+,");
    window.addEventListener("relay-open-settings", openSettings);
    const unsub = bridge.subscribeSnapshot((next) => {
      if (active) snapshot = next;
    });

    void (async () => {
      const [snap, auto] = await Promise.all([bridge.getSnapshot(), bridge.getAutostart()]);
      if (!active) return;
      snapshot ??= snap;
      autostart = auto;
      if (!snap.configured) {
        activeTab = "settings";
      }
    })().catch(err => { if (active) actionError = errorMessage(err); });

    return () => {
      active = false;
      window.removeEventListener("relay-open-settings", openSettings);
      unsub();
    };
  });

  const running = $derived(snapshot?.status.state === "running");
  const draining = $derived(snapshot?.status.state === "draining");
  const listenersEnabled = $derived(snapshot?.config.proxyEnabled || snapshot?.config.telemetryEnabled);
  const hasAttention = $derived(
    snapshot?.status.state === "error" || Boolean(snapshot?.status.outbox?.lastError),
  );

  async function runRelayAction(action: () => Promise<AppSnapshot>, confirmation = "") {
    if (!snapshot || busy || saving) return;
    busy = true;
    actionError = actionConfirmation = "";
    try {
      snapshot = await action();
      actionConfirmation = confirmation;
    } catch (err) {
      actionError = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={handleShortcut} />

<div class="window-shell">
  <header class="titlebar" style="--wails-draggable: drag">
    <div class="brand">
      <img src={appMark} alt="" class="brand-mark" width="22" height="22" />
      <span class="brand-name">LLM Meter Relay</span>
      {#if snapshot}
        <span
          class="status-pill"
          data-state={hasAttention ? "attention" : snapshot.status.state}
        >
          {#if draining}
            Draining…
          {:else if hasAttention}
            Attention
          {:else if running}
            Running
          {:else if !snapshot.configured}
            Setup required
          {:else}
            Stopped
          {/if}
        </span>
      {/if}
    </div>

    {#if snapshot}
      <div class="header-actions" style="--wails-draggable: no-drag">
        <Button
          class={running ? "secondary-button compact-button" : "primary-button compact-button"}
          disabled={busy || saving || draining || (!running && (!snapshot.configured || !listenersEnabled))}
          aria-keyshortcuts="Meta+Enter"
          title={running ? "Stop relay (⌘Return)" : listenersEnabled ? "Start relay (⌘Return)" : "Enable a listener in Settings"}
          onclick={() => runRelayAction(running ? bridge.stopRelay : bridge.startRelay)}
        >
          {#if running}
            <SquareIcon size={11} aria-hidden="true" />
            Stop
          {:else}
            <PlayIcon size={11} aria-hidden="true" />
            Start
          {/if}
        </Button>
      </div>
    {/if}
  </header>

  {#if snapshot}
    <Tabs bind:value={activeTab} class="tabs-root">
      <TabsList aria-label="Relay sections" class="tabs-list">
        <TabsTab value="status" class="tab-btn">
          <TabsTabIcon><ActivityIcon size={14} aria-hidden="true" /></TabsTabIcon>
          Status
        </TabsTab>
        <TabsTab value="settings" class="tab-btn" aria-keyshortcuts="Meta+," title="Settings (⌘,)">
          <TabsTabIcon><SettingsIcon size={14} aria-hidden="true" /></TabsTabIcon>
          Settings
        </TabsTab>
        <TabsTab value="clients" class="tab-btn">
          <TabsTabIcon><TerminalIcon size={14} aria-hidden="true" /></TabsTabIcon>
          Client Setup
        </TabsTab>
        <TabsTab value="logs" class="tab-btn">
          <TabsTabIcon><ScrollTextIcon size={14} aria-hidden="true" /></TabsTabIcon>
          Logs
        </TabsTab>
      </TabsList>

      <main class="panel-scroll">
        {#if actionError}
          <div class="banner banner-error top-err" role="alert">
            <span>{actionError}</span>
          </div>
        {/if}

        {#if actionConfirmation}
          <div class="banner banner-success top-err" role="status">{actionConfirmation}</div>
        {/if}

        <TabsPanel value="status">
          <StatusTab
            {snapshot}
            {busy}
            onOpenSettings={() => (activeTab = "settings")}
            onDrainStop={() => runRelayAction(bridge.drainAndStopRelay)}
            onUploadKept={() => runRelayAction(bridge.uploadKeptReports, "Kept reports uploaded.")}
            onUpdate={(next) => (snapshot = next)}
          />
        </TabsPanel>

        <TabsPanel value="settings" keepMounted>
          <SettingsTab
            {snapshot}
            {autostart}
            bind:saving
            busy={busy || draining}
            onUpdate={(next) => (snapshot = next)}
            onAutostartChange={(next) => (autostart = next)}
          />
        </TabsPanel>

        <TabsPanel value="clients" keepMounted>
          <ClientSetupTab {snapshot} />
        </TabsPanel>

        <TabsPanel value="logs">
          <LogsTab {snapshot} onUpdate={(next) => (snapshot = next)} />
        </TabsPanel>
      </main>
    </Tabs>
  {:else}
    <div class="loading muted" role="status">{actionError || "Loading relay state…"}</div>
  {/if}
</div>

<style>
  .window-shell {
    display: flex;
    flex-direction: column;
    height: 100%;
    background: var(--bg);
  }

  .titlebar {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    height: 52px;
    padding: 0 16px 0 96px;
    background: var(--sidebar);
    border-bottom: 1px solid var(--line);
  }

  .brand {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .brand-mark {
    display: block;
    border-radius: 5px;
    flex: none;
  }

  .brand-name {
    font-weight: 630;
    font-size: 13.5px;
    line-height: 1;
    letter-spacing: -0.015em;
    white-space: nowrap;
  }

  .status-pill {
    display: inline-flex;
    align-items: center;
    font-size: 11px;
    line-height: 1;
    font-weight: 560;
    padding: 4px 8px;
    border-radius: 999px;
    background: var(--surface-raised);
    border: 1px solid var(--line);
    color: var(--muted);
    white-space: nowrap;
  }

  .status-pill[data-state="running"] {
    color: var(--success);
    border-color: var(--success-border);
    background: var(--success-surface);
  }

  .status-pill[data-state="draining"] {
    color: var(--warning);
    border-color: var(--warning-border);
    background: var(--warning-surface);
  }

  .status-pill[data-state="attention"],
  .status-pill[data-state="error"] {
    color: var(--danger);
    border-color: var(--error-border);
    background: var(--error-surface);
  }

  .header-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  :global(.tabs-root) {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
  }

  :global(.tabs-list) {
    flex: none;
    display: flex;
    gap: 4px;
    padding: 8px 16px;
    background: var(--sidebar);
    border-bottom: 1px solid var(--line);
  }

  :global(.tab-btn) {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    min-height: 31px;
    padding: 0 11px;
    border-radius: 6px;
    border: 1px solid transparent;
    background: transparent;
    color: var(--muted);
    font-size: 12.5px;
    line-height: 1;
    font-weight: 530;
    cursor: pointer;
  }

  :global(.tab-btn [data-slot="tabs-tab-icon"]) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
  }

  :global(.tab-btn:hover) {
    background: var(--hover);
    color: var(--text);
  }

  :global(.tab-btn[data-selected]) {
    background: var(--nav-current);
    border-color: var(--selected-border);
    color: var(--nav-current-text);
    font-weight: 600;
  }

  .panel-scroll {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 16px;
  }

  .top-err {
    margin-bottom: 12px;
  }

  .loading {
    flex: 1;
    display: grid;
    place-items: center;
    font-size: 12.5px;
  }
</style>
