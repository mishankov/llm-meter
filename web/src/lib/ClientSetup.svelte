<script lang="ts">
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
  import CopyIcon from '@lucide/svelte/icons/copy';
  import { Button } from '@ui-sdk/svelte/button';
  import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from '@ui-sdk/svelte/collapsible';
  import Filter from './Filter.svelte';
  import { guardEnter } from './format';

  let { origin, disabled }: { origin: string; disabled: boolean } = $props();

  const profiles = [
    { value: 'telemetry', label: 'OpenTelemetry' },
    { value: 'openai', label: 'OpenAI-compatible proxy' }
  ];
  let profile = $state(profiles[0]);
  const endpointURL = $derived(origin + (profile.value === 'telemetry' ? '/otlp' : '/v1'));
  const endpointLabel = $derived(profile.value === 'telemetry' ? 'OTLP endpoint' : 'Base URL');
  let urlInput = $state<HTMLTextAreaElement>();
  let copying = $state(false);
  let copyMessage = $state('');

  function changeProfile(value: string) {
    const selected = profiles.find((option) => option.value === value);
    if (!selected || selected.value === profile.value) return;
    profile = selected;
    copyMessage = '';
  }

  async function copyURL() {
    if (disabled || copying || !origin || !urlInput) return;
    const url = endpointURL;
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
    <label class="setup-label" for="setup-base-url">{endpointLabel}</label>
    <div class="url-row">
      <textarea id="setup-base-url" class="text-input" dir="ltr" value={endpointURL} rows="2" readonly spellcheck="false" bind:this={urlInput}></textarea>
      <Button class="secondary-button" title="Copy URL (Enter)" aria-keyshortcuts="Enter" disabled={disabled || copying || !origin} focusableWhenDisabled onclick={copyURL} onkeydown={guardEnter}>
        <CopyIcon size={14} aria-hidden="true" /> Copy URL
      </Button>
    </div>
    <p class="copy-feedback muted" role="status">{copyMessage}</p>
  </div>

  {#if profile.value === 'telemetry'}
    <p class="credentials muted">Send your proxy key in <code>X-LLM-Meter-Key</code> for direct OTLP/HTTP export from Claude Code, Codex, Gemini CLI, GitHub Copilot CLI, or OpenCode (Copilot CLI requires HTTPS). Use the local relay for local filtering, durable buffering, or clients without custom header support.</p>
    <a class="guide-link" href="https://github.com/mishankov/llm-meter/blob/main/docs/configuration.md#53-opentelemetry" target="_blank" rel="noreferrer">OpenTelemetry setup guide</a>
  {:else}
    <p class="credentials muted">Send your proxy key in <code>X-LLM-Meter-Key</code>. Keep your provider API key in <code>Authorization</code>.</p>

    <Collapsible class="setup-help">
      <CollapsibleTrigger class="secondary-button" title="Provider setup instructions (Enter)" aria-keyshortcuts="Enter" onkeydown={guardEnter}>
        Provider setup instructions <ChevronDownIcon size={14} aria-hidden="true" />
      </CollapsibleTrigger>
      <CollapsiblePanel>
        <div class="instructions muted">
          <p>Set the full API base URL above, including <code>/v1</code>. For clients that can’t send custom headers, use the local proxy relay.</p>
        </div>
      </CollapsiblePanel>
    </Collapsible>
  {/if}
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
  .guide-link { color: var(--accent); justify-self: start; }
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
