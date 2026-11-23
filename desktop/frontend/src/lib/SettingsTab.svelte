<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import { Button } from "@ui-sdk/svelte/button";
  import { Checkbox, CheckboxIndicator } from "@ui-sdk/svelte/checkbox";
  import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@ui-sdk/svelte/collapsible";
  import {
    Dialog,
    DialogBackdrop,
    DialogClose,
    DialogContent,
    DialogDescription,
    DialogTitle,
  } from "@ui-sdk/svelte/dialog";
  import { Radio, RadioGroup, RadioIndicator } from "@ui-sdk/svelte/radio";
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectItemIndicator,
    SelectTrigger,
    SelectValue,
  } from "@ui-sdk/svelte/select";
  import {
    bridge,
    errorMessage,
    type AppConfig,
    type AppSnapshot,
    type AutostartStatus,
  } from "./bridge";

  let {
    snapshot,
    autostart,
    saving = $bindable(false),
    busy,
    onUpdate,
    onAutostartChange,
  }: {
    snapshot: AppSnapshot;
    autostart: AutostartStatus;
    saving: boolean;
    busy: boolean;
    onUpdate: (snap: AppSnapshot) => void;
    onAutostartChange: (st: AutostartStatus) => void;
  } = $props();

  let target = $state("");
  let targetInput: HTMLInputElement;
  const targetError = $derived.by(() => {
    if (!target.trim()) return "";
    try {
      const url = new URL(target.trim());
      if (url.protocol === "https:" && url.host && !url.username && !url.password &&
          url.pathname === "/" && !url.search && !url.hash) return "";
    } catch { /* Report incomplete URLs while editing. */ }
    return "Enter an HTTPS origin without a path, query, fragment, or credentials.";
  });
  $effect(() => { targetInput?.setCustomValidity(targetError); });
  let mode = $state("usage-only");
  let listen = $state("127.0.0.1:8765");
  let provider = $state("");
  let upstream = $state("");
  let autoStart = $state(true);
  let newKey = $state("");
  let initialized = $state(false);

  let settingLogin = $state(false);
  let testing = $state(false);
  let testFeedback = $state<{ ok: boolean; text: string } | null>(null);
  let saveFeedback = $state<{ ok: boolean; text: string } | null>(null);
  let drainDialogOpen = $state(false);

  $effect(() => {
    if (!initialized && snapshot) {
      syncFromSnapshot(snapshot);
      initialized = true;
    }
  });

  function syncFromSnapshot(snap: AppSnapshot) {
    target = snap.config.target || "";
    mode = snap.config.mode || "usage-only";
    listen = snap.config.listen || "127.0.0.1:8765";
    provider = snap.config.provider || "";
    upstream = snap.config.upstream || "";
    autoStart = snap.config.autoStart;
  }

  const providerOptions = [
    { value: "", label: "All 4 providers (recommended)" },
    { value: "antigravity", label: "Antigravity only" },
    { value: "openai", label: "OpenAI-compatible (/v1) only" },
    { value: "codex", label: "Signed-in Codex (/codex) only" },
    { value: "opencode-go", label: "OpenCode Go (/opencode-go) only" },
  ];

  const modeChoices = [
    {
      value: "usage-only",
      label: "Usage-only",
      desc: "Model traffic and credentials go directly to providers; relay uploads usage metadata to LLM Meter.",
    },
    {
      value: "remote-proxy",
      label: "Remote proxy",
      desc: "Relay forwards model traffic through your LLM Meter server, which meters and forwards to providers.",
    },
  ];

  const pendingOutbox = $derived(snapshot.status.outbox?.pending ?? snapshot.pendingOnDisk);
  const switchingOutbox = $derived(
    snapshot.config.mode === "usage-only" &&
      (mode !== "usage-only" ||
        target.trim() !== snapshot.config.target ||
        newKey.trim() !== ""),
  );

  function buildConfig(): AppConfig {
    return {
      target: target.trim(),
      mode,
      listen: listen.trim() || "127.0.0.1:8765",
      provider,
      upstream: mode === "usage-only" && provider !== "" ? upstream.trim() : "",
      autoStart,
    };
  }

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    if (saving || busy || drainDialogOpen) return;
    saveFeedback = null;

    if (switchingOutbox && pendingOutbox > 0) {
      drainDialogOpen = true;
      return;
    }
    await persist(switchingOutbox);
  }

  async function persist(drainBeforeSwitch: boolean) {
    if (busy || saving) return;
    drainDialogOpen = false;
    saving = true;
    saveFeedback = null;
    try {
      const updated = await bridge.saveConfig({
        config: buildConfig(),
        newKey: newKey.trim(),
        drainBeforeSwitch,
        startAfterSave: !snapshot.configured,
      });
      newKey = "";
      syncFromSnapshot(updated);
      onUpdate(updated);
      saveFeedback = { ok: true, text: "Settings saved." };
    } catch (err) {
      saveFeedback = { ok: false, text: errorMessage(err) };
    } finally {
      saving = false;
    }
  }

  async function testServer() {
    testing = true;
    testFeedback = null;
    try {
      await bridge.testConnection(target.trim());
      testFeedback = { ok: true, text: "Server responded 204 No Content on /healthz." };
    } catch (err) {
      testFeedback = { ok: false, text: errorMessage(err) };
    } finally {
      testing = false;
    }
  }

  async function toggleAutostart(next: boolean) {
    settingLogin = true;
    try {
      const st = await bridge.setAutostart(next);
      onAutostartChange(st);
    } catch (err) {
      saveFeedback = { ok: false, text: errorMessage(err) };
    } finally {
      settingLogin = false;
    }
  }
</script>

<form onsubmit={handleSubmit}>
<fieldset class="form-stack" disabled={saving || busy}>

  <section class="card form-section">
    <div class="field">
      <label class="field-label" for="relay-target">LLM Meter server URL</label>
      <div class="row-with-btn">
        <input
          id="relay-target"
          class="text-input mono-input"
          type="url"
          placeholder="https://proxy.example.com"
          bind:this={targetInput}
          bind:value={target}
          aria-invalid={!!targetError}
          aria-describedby="target-hint"
          required
          spellcheck="false"
        />
        <Button
          type="button"
          class="secondary-button"
          disabled={testing || !target.trim() || !!targetError}
          aria-keyshortcuts="Meta+Shift+T" title="Test server (⌘⇧T)"
          onclick={testServer}
        >
          {testing ? "Testing…" : "Test"}
        </Button>
      </div>
      {#if targetError}
        <p class="field-hint err-text" id="target-hint">{targetError}</p>
      {:else if testFeedback}
        <p class="field-hint" id="target-hint" class:ok-text={testFeedback.ok} class:err-text={!testFeedback.ok} role="status">
          {testFeedback.text}
        </p>
      {:else}
        <p class="field-hint muted" id="target-hint">HTTPS origin without a path, query, or credentials.</p>
      {/if}
    </div>

    <div class="field">
      <label class="field-label" for="relay-key">
        Proxy key
        {#if snapshot.keyConfigured}
          <span class="saved-badge mono-input">Saved: {snapshot.keyPrefix}</span>
        {/if}
      </label>
      <input
        id="relay-key"
        class="text-input mono-input"
        type="password"
        autocomplete="off"
        placeholder={snapshot.keyConfigured ? "Leave blank to keep saved key, or paste a new llmm_… key" : "llmm_…"}
        bind:value={newKey}
        required={!snapshot.keyConfigured}
        spellcheck="false"
      />
      <p class="field-hint muted">Saved in your macOS Keychain.</p>
    </div>

    <div class="field">
      <span class="field-label" id="relay-mode-label">Relay mode</span>
      <RadioGroup
        aria-labelledby="relay-mode-label"
        value={mode}
        onValueChange={(next) => {
          if (next) mode = next;
        }}
        class="mode-grid"
      >
        {#each modeChoices as choice (choice.value)}
          <label class="mode-card" class:selected={mode === choice.value}>
            <div class="mode-card-head">
              <Radio value={choice.value} class="radio-btn">
                <RadioIndicator class="radio-dot" />
              </Radio>
              <span class="mode-title">{choice.label}</span>
            </div>
            <span class="mode-desc muted">{choice.desc}</span>
          </label>
        {/each}
      </RadioGroup>
    </div>

    <div class="field">
      <label class="field-label" for="relay-listen">Local loopback listen address</label>
      <input
        id="relay-listen"
        class="text-input mono-input"
        type="text"
        placeholder="127.0.0.1:8765"
        bind:value={listen}
        spellcheck="false"
      />
      <p class="field-hint muted">Loopback only (127.0.0.1 or [::1]). Clients must connect using this exact host and port.</p>
    </div>
  </section>

  <section class="card form-section">
    <label class="checkbox-row">
      <Checkbox
        class="checkbox-box"
        checked={autoStart}
        onCheckedChange={(next) => (autoStart = next === true)}
      >
        <CheckboxIndicator>
          <CheckIcon size={11} aria-hidden="true" />
        </CheckboxIndicator>
      </Checkbox>
      <span>Start relay automatically when the app opens</span>
    </label>

    {#if autostart.available}
      <label class="checkbox-row">
        <Checkbox
          class="checkbox-box"
          checked={autostart.enabled}
          disabled={settingLogin}
          onCheckedChange={(next) => toggleAutostart(next === true)}
        >
          <CheckboxIndicator>
            <CheckIcon size={11} aria-hidden="true" />
          </CheckboxIndicator>
        </Checkbox>
        <span>Launch LLM Meter Relay at macOS login</span>
      </label>
    {/if}

    <Collapsible class="advanced">
      <CollapsibleTrigger class="secondary-button compact-button">
        Advanced routing
        <ChevronDownIcon size={13} aria-hidden="true" />
      </CollapsibleTrigger>
      <CollapsiblePanel class="advanced-panel">
        <div class="field">
          <span class="field-label" id="provider-scope-label">Provider scope</span>
          <Select
            items={providerOptions}
            value={provider}
            onValueChange={(next) => {
              provider = next ?? "";
              if (!provider) upstream = "";
            }}
          >
            <SelectTrigger class="select-trigger" aria-labelledby="provider-scope-label">
              <SelectValue />
              <ChevronDownIcon size={14} aria-hidden="true" />
            </SelectTrigger>
            <SelectContent class="select-content">
              {#each providerOptions as opt (opt.value)}
                <SelectItem class="select-item" value={opt.value}>
                  {opt.label}
                  <SelectItemIndicator>
                    <CheckIcon size={13} aria-hidden="true" />
                  </SelectItemIndicator>
                </SelectItem>
              {/each}
            </SelectContent>
          </Select>
        </div>

        {#if mode === "usage-only" && provider !== ""}
          <div class="field">
            <label class="field-label" for="relay-upstream">Custom HTTPS provider upstream (optional)</label>
            <input
              id="relay-upstream"
              class="text-input mono-input"
              type="url"
              placeholder="https://openrouter.ai/api/v1"
              bind:value={upstream}
              spellcheck="false"
            />
            <p class="field-hint muted">Overrides the default upstream for the selected provider in usage-only mode.</p>
          </div>
        {/if}
      </CollapsiblePanel>
    </Collapsible>
  </section>

  <div class="form-footer">
    {#if saveFeedback}
      <div class="banner" class:banner-success={saveFeedback.ok} class:banner-error={!saveFeedback.ok} role="status">
        <span>{saveFeedback.text}</span>
      </div>
    {/if}
    <Button type="button" class="secondary-button" disabled={saving} onclick={() => { syncFromSnapshot(snapshot); newKey = ""; saveFeedback = null; }}>
      Reset
    </Button>
    <Button type="submit" class="primary-button" disabled={saving || busy} title="Save settings (⌘S)" aria-keyshortcuts="Meta+S">
      {saving ? "Saving…" : snapshot.configured ? "Save & Apply" : "Save & Start Relay"}
    </Button>
  </div>
</fieldset>
</form>

<Dialog bind:open={drainDialogOpen}>
  <DialogBackdrop class="dialog-backdrop" />
  <DialogContent class="dialog-content">
    <DialogTitle class="dialog-title">Pending usage reports in outbox</DialogTitle>
    <DialogDescription class="dialog-desc">
      There {pendingOutbox === 1 ? "is 1 pending report" : `are ${pendingOutbox} pending reports`} queued for the current
      server and key. Drain before switching, or upload the kept reports later from Status.
    </DialogDescription>
    <div class="dialog-actions">
      <DialogClose class="secondary-button">Cancel</DialogClose>
      <Button class="secondary-button" disabled={saving || busy} onclick={() => persist(false)}>
        Switch and keep old queue
      </Button>
      <Button class="primary-button" disabled={saving || busy} title="Drain and apply (⌘Return)" aria-keyshortcuts="Meta+Enter" onclick={() => persist(true)}>
        Drain &amp; Apply
      </Button>
    </div>
  </DialogContent>
</Dialog>

<style>
  .form-stack {
    margin: 0;
    padding: 0;
    min-width: 0;
    border: 0;
    display: grid;
    gap: 14px;
  }

  .form-section {
    display: grid;
    gap: 14px;
  }

  .field {
    display: grid;
    gap: 6px;
  }

  .field-label {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
    font-weight: 560;
  }

  .saved-badge {
    font-size: 11px;
    font-weight: 480;
    color: var(--success);
  }

  .row-with-btn {
    display: flex;
    gap: 8px;
  }

  .row-with-btn input {
    flex: 1;
  }

  .field-hint {
    margin: 0;
    font-size: 11.5px;
    line-height: 1.4;
  }

  .ok-text {
    color: var(--success);
  }

  .err-text {
    color: var(--danger);
  }

  :global(.mode-grid) {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }

  .mode-card {
    display: grid;
    gap: 6px;
    align-content: start;
    padding: 11px 12px;
    border-radius: 8px;
    border: 1px solid var(--line);
    background: var(--bg);
    cursor: pointer;
  }

  .mode-card.selected {
    border-color: var(--selected-border);
    background: var(--nav-current);
  }

  .mode-card-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  :global(.radio-btn) {
    inline-size: 16px;
    block-size: 16px;
    border-radius: 50%;
    border: 1px solid var(--control-border);
    background: var(--bg);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    flex: none;
  }

  :global(.radio-btn[data-checked]) {
    border-color: var(--accent);
  }

  :global(.radio-dot) {
    display: block;
    inline-size: 8px;
    block-size: 8px;
    border-radius: 50%;
    background: var(--accent);
  }

  .mode-title {
    font-size: 12.5px;
    line-height: 1.25;
    font-weight: 600;
  }

  .mode-desc {
    font-size: 11.5px;
    line-height: 1.45;
  }

  :global(.advanced [aria-expanded="true"] svg) {
    transform: rotate(180deg);
  }

  :global(.advanced-panel) {
    display: grid;
    gap: 12px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--line);
  }

  .form-footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    align-items: center;
    position: sticky;
    bottom: -16px;
    padding: 10px 0;
    background: var(--bg);
    border-top: 1px solid var(--line);
  }

  .form-footer .banner {
    flex: 1;
    margin: 0;
    padding: 6px 8px;
  }
</style>
