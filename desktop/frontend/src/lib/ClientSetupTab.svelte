<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import CopyIcon from "@lucide/svelte/icons/copy";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
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

  const clients = [
    { value: "codex", label: "Signed-in Codex" },
    { value: "opencode-api", label: "OpenCode · OpenAI API key" },
    { value: "opencode-chatgpt", label: "OpenCode · ChatGPT sign-in" },
    { value: "opencode-go", label: "OpenCode Go" },
    { value: "antigravity", label: "Antigravity / T3 Code" },
    { value: "openai", label: "OpenAI-compatible SDK / CLI" },
  ];

  const scope = $derived(snapshot.config.provider);
  const clientChoices = $derived(clients.filter(c =>
    !scope || c.value === scope || (scope === "codex" && c.value === "opencode-chatgpt") ||
      (scope === "openai" && c.value === "opencode-api"),
  ));
  let preferredClient = $state("codex");
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

  const isOpenCode = $derived(selectedClient?.startsWith("opencode"));
  const baseUrl = $derived.by(() => {
    switch (selectedClient) {
      case "codex":
      case "opencode-chatgpt":
        return `${origin}/codex`;
      case "opencode-go":
        return `${origin}/opencode-go`;
      case "antigravity":
        return origin;
      default:
        return `${origin}/v1`;
    }
  });

  const snippet = $derived.by(() => {
    switch (selectedClient) {
      case "codex":
        return `model_provider = "metered-codex"

[model_providers.metered-codex]
name = "OpenAI"
base_url = "${origin}/codex"
wire_api = "responses"
requires_openai_auth = true
supports_websockets = true`;
      case "opencode-api":
      case "opencode-chatgpt":
      case "opencode-go": {
        const provider = selectedClient === "opencode-go" ? "opencode-go" : "openai";
        return JSON.stringify({ providers: { [provider]: { settings: { baseURL: baseUrl } } } }, null, 2);
      }
      case "antigravity":
        return launcherScript;
      default:
        return `export OPENAI_BASE_URL="${origin}/v1"
# Keep your normal OPENAI_API_KEY; do not send X-LLM-Meter-Key.`;
    }
  });

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
        <Button class="secondary-button compact-button" onclick={() => copy("url", baseUrl)}>
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

    {#if installResult || scriptError}
      <div class="banner" class:banner-success={installResult?.ok && !scriptError} class:banner-error={!installResult?.ok || scriptError} role="status">
        <span>{scriptError || installResult?.text}</span>
      </div>
    {/if}
    <div class="field">
      <div class="snippet-head">
        <span class="field-label">
          {#if selectedClient === "codex"}
            Codex <code>config.toml</code>
          {:else if isOpenCode}
            OpenCode <code>opencode.jsonc</code>
          {:else if selectedClient === "antigravity"}
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
  </section>
</div>

<style>
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
