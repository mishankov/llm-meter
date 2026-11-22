<script lang="ts">
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
  import CopyIcon from '@lucide/svelte/icons/copy';
  import { Button } from '@ui-sdk/svelte/button';
  import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from '@ui-sdk/svelte/collapsible';
  import Filter from './Filter.svelte';
  import { guardEnter } from './format';

  let { origin, disabled }: { origin: string; disabled: boolean } = $props();

  const profiles = [
    { value: 'openai', label: 'OpenAI-compatible client', path: '/v1' },
    { value: 'codex', label: 'Codex (ChatGPT sign-in)', path: '/codex' },
    { value: 'opencode-chatgpt', label: 'OpenCode (ChatGPT sign-in)', path: '/codex' },
    { value: 'opencode-go', label: 'OpenCode Go', path: '/opencode-go' }
  ];
  let profile = $state(profiles[0]);
  const baseURL = $derived(origin + profile.path);
  let urlInput: HTMLTextAreaElement;
  let copying = $state(false);
  let copyMessage = $state('');

  function changeProfile(value: string) {
    const selected = profiles.find((option) => option.value === value);
    if (!selected || selected.value === profile.value) return;
    profile = selected;
    copyMessage = '';
  }

  async function copyURL() {
    if (disabled || copying || !origin) return;
    const url = baseURL;
    const copiedProfile = profile.value;
    const field = urlInput;
    copyMessage = '';
    copying = true;
    try {
      await navigator.clipboard.writeText(url);
      if (field.isConnected && profile.value === copiedProfile) copyMessage = 'URL copied.';
    } catch {
      if (field.isConnected && profile.value === copiedProfile && !disabled) {
        field.focus();
        field.select();
        copyMessage = 'Use your copy shortcut to copy the selected URL.';
      }
    } finally {
      copying = false;
    }
  }
</script>

<div class="setup">
  <div class="setup-field">
    <span class="setup-label">Connection type</span>
    <Filter label="Connection type" value={profile.value} items={profiles} onChange={changeProfile} />
  </div>

  <div class="setup-field">
    <label class="setup-label" for="setup-base-url">Base URL</label>
    <div class="url-row">
      <textarea id="setup-base-url" class="text-input" dir="ltr" value={baseURL} rows="2" readonly spellcheck="false" bind:this={urlInput}></textarea>
      <Button class="secondary-button" title="Copy URL (Enter)" aria-keyshortcuts="Enter" disabled={disabled || copying || !origin} focusableWhenDisabled onclick={copyURL} onkeydown={guardEnter}>
        <CopyIcon size={14} aria-hidden="true" /> Copy URL
      </Button>
    </div>
    <p class="copy-feedback muted" role="status">{copyMessage}</p>
  </div>

  <p class="credentials muted">
    Send your proxy key in <code>X-LLM-Meter-Key</code>.
    {#if profile.value === 'openai'}
      Keep your provider API key in <code>Authorization</code>.
    {:else if profile.value === 'opencode-go'}
      Keep your saved OpenCode Go credentials.
    {:else}
      Keep your ChatGPT sign-in.
    {/if}
  </p>

  <Collapsible class="setup-help">
    <CollapsibleTrigger class="secondary-button" title="Provider setup instructions (Enter)" aria-keyshortcuts="Enter" onkeydown={guardEnter}>
      Provider setup instructions <ChevronDownIcon size={14} aria-hidden="true" />
    </CollapsibleTrigger>
    <CollapsiblePanel>
      <div class="instructions muted">
        {#if profile.value === 'openai'}
          <p>Set the API base URL in your client. For OpenCode, set <code>settings.baseURL</code> on the <code>openai</code> provider in <code>opencode.jsonc</code>. Add <code>X-LLM-Meter-Key</code> to the provider’s <code>headers</code>.</p>
        {:else if profile.value === 'codex'}
          <p>In Codex’s <code>config.toml</code>, select a provider with this <code>base_url</code>. Set <code>wire_api = "responses"</code>, <code>requires_openai_auth = true</code>, and <code>supports_websockets = true</code>. Map <code>X-LLM-Meter-Key</code> to <code>LLM_METER_KEY</code> in <code>env_http_headers</code>, set that environment variable to your proxy key, and start a new Codex session.</p>
        {:else if profile.value === 'opencode-chatgpt'}
          <p>In <code>opencode.jsonc</code>, set <code>settings.baseURL</code> on the <code>openai</code> provider. Add <code>X-LLM-Meter-Key</code> to the provider’s <code>headers</code>.</p>
        {:else}
          <p>In <code>opencode.jsonc</code>, set <code>settings.baseURL</code> on the <code>opencode-go</code> provider. Add <code>X-LLM-Meter-Key</code> to the provider’s <code>headers</code>.</p>
        {/if}
      </div>
    </CollapsiblePanel>
  </Collapsible>
</div>

<style>
  .setup { display: grid; gap: 10px; align-items: start; }
  .setup-field { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; min-inline-size: 0; }
  .setup-label { color: var(--muted); font-size: 12px; font-weight: 550; }
  .setup-field :global(.filter-trigger) { justify-self: start; }
  .url-row { display: flex; align-items: start; gap: 6px; }
  .url-row textarea { flex: 1; min-inline-size: 0; resize: none; overflow-wrap: anywhere; font: 13px/1.4 var(--mono); }
  .url-row textarea:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .url-row :global(button) { flex: none; }
  .copy-feedback, .credentials, .instructions p { margin: 0; }
  .copy-feedback:empty { position: absolute; }
  .setup code { color: var(--text); overflow-wrap: anywhere; }
  .setup :global(.setup-help [aria-expanded=true] svg) { transform: rotate(180deg); }
  .instructions { display: grid; gap: 7px; margin-block-start: 8px; }

  @media (max-width: 760px) {
    .url-row textarea, .url-row :global(button), .setup-field :global(.filter-trigger) { min-block-size: 44px; }
    .url-row textarea { font-size: 16px; }
  }
  @media (max-width: 340px) {
    .url-row textarea { min-block-size: 86px; }
  }
</style>
