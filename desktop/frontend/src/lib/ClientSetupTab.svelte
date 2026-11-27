<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import CopyIcon from "@lucide/svelte/icons/copy";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import { Browser } from "@wailsio/runtime";
  import { Button } from "@ui-sdk/svelte/button";
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectItemIndicator,
    SelectTrigger,
    SelectValue,
  } from "@ui-sdk/svelte/select";
  import { bridge, errorMessage, type AppSnapshot } from "./bridge";

  let { snapshot }: { snapshot: AppSnapshot } = $props();

  const guide = "https://github.com/mishankov/llm-meter/blob/main/docs/configuration.md";
  const telemetryGuide = guide + "#53-opentelemetry";
  const migrationGuide = guide + "#migrating-from-the-codex-and-opencode-go-proxy-routes";
  const clients = [
    { value: "openai", label: "OpenAI-compatible SDK / CLI" },
    { value: "antigravity", label: "Antigravity / T3 Code" },
  ];

  const proxy = $derived(snapshot.config.proxyEnabled);
  const telemetry = $derived(snapshot.config.telemetryEnabled);
  const telemetryURL = $derived(snapshot.endpoints?.find(endpoint => endpoint.provider === "telemetry")?.url ?? "");
  const scope = $derived(snapshot.config.provider);
  const clientChoices = $derived(proxy ? clients.filter(client => !scope || client.value === scope) : []);
  let preferredClient = $state("openai");
  const selectedClient = $derived(clientChoices.some(c => c.value === preferredClient)
    ? preferredClient : clientChoices[0]?.value);

  let antigravityBin = $state("");
  let launcherDest = $state("");
  let launcherScript = $state("");
  let copiedKey = $state("");
  let installResult = $state<{ ok: boolean; text: string } | null>(null);
  let installing = $state(false);
  let picking = $state(false);
  let scriptError = $state("");
  let syncedDefaults = $state(false);

  const listen = $derived(snapshot.status.listenAddr || snapshot.config.listen || "127.0.0.1:8765");
  const origin = $derived(`http://${listen}`);

  $effect(() => {
    if (!syncedDefaults && snapshot) {
      antigravityBin = snapshot.antigravityBin || "";
      launcherDest = snapshot.launcherPath || "";
      syncedDefaults = true;
    }
  });

  $effect(() => {
    // The native generator reads the current listener when it builds the script.
    const listener = listen;
    launcherScript = "";
    scriptError = "";
    if (selectedClient !== "antigravity" || !antigravityBin.trim() || !listener) return;
    let current = true;
    bridge.generateAntigravityLauncher(antigravityBin).then(
      script => { if (current) launcherScript = script; },
      err => { if (current) scriptError = errorMessage(err); },
    );
    return () => { current = false; };
  });

  const baseUrl = $derived(selectedClient === "antigravity" ? `${origin}/antigravity` : `${origin}/v1`);
  const snippet = $derived(selectedClient === "antigravity" ? launcherScript : `export OPENAI_BASE_URL="${baseUrl}"
# Keep your normal OPENAI_API_KEY; do not send X-LLM-Meter-Key.`);

  function openGuide(event: MouseEvent, url: string) {
    event.preventDefault();
    void Browser.OpenURL(url).catch(err => {
      installResult = { ok: false, text: errorMessage(err) };
    });
  }

  async function copy(key: string, value: string) {
    try {
      await bridge.copyText(value);
    } catch (err) {
      installResult = { ok: false, text: errorMessage(err) };
      return;
    }
    copiedKey = key;
    setTimeout(() => {
      if (copiedKey === key) copiedKey = "";
    }, 1800);
  }

  async function pickBinary() {
    picking = true;
    installResult = null;
    try {
      const picked = await bridge.pickAntigravityBinary();
      if (picked) {
        antigravityBin = picked;
      }
    } catch (err) {
      installResult = { ok: false, text: errorMessage(err) };
    } finally {
      picking = false;
    }
  }

  async function installLauncher() {
    installing = true;
    installResult = null;
    try {
      const written = await bridge.installAntigravityLauncher(launcherDest.trim(), antigravityBin.trim());
      launcherDest = written;
      installResult = {
        ok: true,
        text: `Installed executable launcher at ${written}. Set T3 Code → Settings → Providers → Antigravity → Binary path to this file.`,
      };
    } catch (err) {
      installResult = { ok: false, text: errorMessage(err) };
    } finally {
      installing = false;
    }
  }
</script>

<div class="stack">
  {#if telemetry}
    <section class="card setup-card">
      <div class="field">
        <span class="field-label">OTLP/HTTP base URL</span>
        <div class="url-row">
          <code class="url-box">{telemetryURL}</code>
          <Button
            class="secondary-button"
            disabled={!telemetryURL}
            title="Copy endpoint (⌘⇧E)"
            aria-keyshortcuts="Meta+Shift+E"
            onclick={() => copy("telemetry", telemetryURL)}
          >
            <CopyIcon size={12} aria-hidden="true" />
            {copiedKey === "telemetry" ? "Copied" : "Copy endpoint"}
          </Button>
        </div>
        <p class="hint muted">Use this for <code>OTEL_EXPORTER_OTLP_ENDPOINT</code> (already ends in <code>/otlp</code>). For signal-specific settings, append <code>/v1/logs</code> or <code>/v1/traces</code>.</p>
      </div>
      <p class="hint muted">Configure Claude Code, Codex, Gemini CLI, GitHub Copilot CLI, or OpenCode using the <a class="guide-link" href={telemetryGuide} onclick={(event) => openGuide(event, telemetryGuide)}>OpenTelemetry setup guide</a>.</p>
    </section>
  {/if}

  {#if selectedClient}
    <section class="card setup-card">
      <div class="field">
        <span class="field-label" id="client-select-label">Client</span>
        <Select
          items={clientChoices}
          value={selectedClient}
          onValueChange={(next) => {
            if (next) preferredClient = next;
            installResult = null;
          }}
        >
          <SelectTrigger class="select-trigger" aria-labelledby="client-select-label">
            <SelectValue />
            <ChevronDownIcon size={14} aria-hidden="true" />
          </SelectTrigger>
          <SelectContent class="select-content">
            {#each clientChoices as choice (choice.value)}
              <SelectItem class="select-item" value={choice.value}>
                {choice.label}
                <SelectItemIndicator>
                  <CheckIcon size={13} aria-hidden="true" />
                </SelectItemIndicator>
              </SelectItem>
            {/each}
          </SelectContent>
        </Select>
      </div>

      <div class="field">
        <span class="field-label">Local base URL</span>
        <div class="url-row">
          <code class="url-box">{baseUrl}</code>
          <Button class="secondary-button" onclick={() => copy("url", baseUrl)}>
            <CopyIcon size={12} aria-hidden="true" />
            {copiedKey === "url" ? "Copied" : "Copy URL"}
          </Button>
        </div>
        <p class="hint muted">
          Use the exact host and port shown. The relay checks both on incoming requests.
        </p>
      </div>

      {#if selectedClient === "antigravity"}
        <div class="launcher-box">
          <div class="field">
            <label class="field-label" for="ag-bin">Installed Antigravity executable</label>
            <div class="url-row">
              <input
                id="ag-bin"
                class="text-input mono-input"
                type="text"
                placeholder="/Applications/Antigravity.app/Contents/Resources/app/bin/antigravity"
                disabled={picking || installing}
                bind:value={antigravityBin}
                spellcheck="false"
              />
              <Button type="button" class="secondary-button" disabled={picking || installing} onclick={pickBinary}>
                <FolderOpenIcon size={13} aria-hidden="true" />
                Browse…
              </Button>
            </div>
          </div>

          <div class="field">
            <label class="field-label" for="ag-dest">Launcher destination (named <code>antigravity</code> or <code>agy</code>)</label>
            <div class="url-row">
              <input
                id="ag-dest"
                class="text-input mono-input"
                type="text"
                placeholder="~/.local/bin/antigravity"
                disabled={installing}
                bind:value={launcherDest}
                spellcheck="false"
              />
              <Button type="button" class="primary-button" title="Install launcher (⌘⇧I)" aria-keyshortcuts="Meta+Shift+I" disabled={installing || picking || !launcherScript || !launcherDest.trim()} onclick={installLauncher}>
                <TerminalIcon size={13} aria-hidden="true" />
                {installing ? "Installing…" : "Install Launcher"}
              </Button>
            </div>
          </div>


        </div>
      {/if}

      <div class="field">
        <div class="snippet-head">
          <span class="field-label">
            {#if selectedClient === "antigravity"}
              Launcher script preview
            {:else}
              Environment example
            {/if}
          </span>
          <Button class="secondary-button compact-button" disabled={!snippet} title="Copy snippet (⌘⇧C)" aria-keyshortcuts="Meta+Shift+C" onclick={() => copy("snippet", snippet)}>
            <CopyIcon size={12} aria-hidden="true" />
            {copiedKey === "snippet" ? "Copied" : "Copy Snippet"}
          </Button>
        </div>
        <pre class="code-block"><code>{snippet}</code></pre>
      </div>
      {#if !telemetry}
        <p class="hint muted">To receive harness telemetry, enable OpenTelemetry in Settings and follow the <a class="guide-link" href={telemetryGuide} onclick={(event) => openGuide(event, telemetryGuide)}>OpenTelemetry setup guide</a>.</p>
      {/if}
    </section>
  {:else if proxy}
    <section class="card setup-card">
      <p class="hint muted">This relay profile uses a retired proxy route. <a class="guide-link" href={migrationGuide} onclick={(event) => openGuide(event, migrationGuide)}>Migrate to OpenTelemetry</a> or choose a supported proxy route in Settings.</p>
    </section>
  {:else if !telemetry}
    <section class="card setup-card">
      <p class="hint muted">Enable a listener in Settings to see its client setup.</p>
    </section>
  {/if}
  {#if installResult || scriptError}
    <div class="banner" class:banner-success={installResult?.ok && !scriptError} class:banner-error={!installResult?.ok || scriptError} role="status">
      <span>{scriptError || installResult?.text}</span>
    </div>
  {/if}
</div>

<style>
  .guide-link {
    color: var(--accent);
  }
  .stack {
    display: grid;
    gap: 14px;
  }

  .setup-card {
    display: grid;
    gap: 14px;
  }

  .field {
    display: grid;
    min-width: 0;
    gap: 6px;
  }

  .field-label {
    font-size: 12px;
    font-weight: 560;
  }

  .url-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .url-box {
    flex: 1;
    min-height: 34px;
    display: flex;
    align-items: center;
    padding: 7px 10px;
    border-radius: 6px;
    background: var(--bg);
    border: 1px solid var(--line);
    font-size: 12px;
    overflow-wrap: anywhere;
    white-space: pre-line;
  }

  .hint {
    margin: 0;
    font-size: 11.5px;
    line-height: 1.45;
  }

  .launcher-box {
    display: grid;
    gap: 10px;
    padding: 12px;
    border-radius: 7px;
    background: var(--sidebar);
    border: 1px solid var(--line);
  }

  .snippet-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .code-block {
    margin: 0;
    padding: 11px 12px;
    border-radius: 6px;
    background: var(--bg);
    border: 1px solid var(--line);
    font-size: 11.5px;
    line-height: 1.5;
    overflow-x: auto;
    white-space: pre;
  }
</style>
