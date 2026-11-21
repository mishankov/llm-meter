<script lang="ts">
  import CircleAlertIcon from '@lucide/svelte/icons/circle-alert';
  import { Button } from '@ui-sdk/svelte/button';
  import type { DashboardProblem } from './api';
  import { guardEnter } from './format';

  interface Props {
    problem: DashboardProblem;
    retry?: () => void;
    busy?: boolean;
    cached?: boolean;
    retryLabel?: string;
    cachedMessage?: string;
  }

  let {
    problem, retry, busy = false, cached = false, retryLabel = 'Retry',
    cachedMessage = 'Showing the last loaded data for this view.'
  }: Props = $props();
</script>

<div class="recovery feedback error-feedback" role="alert">
  <CircleAlertIcon size={16} aria-hidden="true" />
  <div class="recovery-copy">
    <strong>{problem.title}</strong>
    <p>{problem.detail}</p>
    {#if cached}<p class="cached">{cachedMessage}</p>{/if}
  </div>
  {#if retry}
    <Button class="secondary-button" title={retryLabel + ' (Enter)'} aria-keyshortcuts="Enter" disabled={busy} focusableWhenDisabled onclick={retry} onkeydown={guardEnter}>{retryLabel}</Button>
  {/if}
</div>

<style>
  .recovery { align-items: start; justify-content: start; flex-wrap: wrap; padding: 12px; }
  .recovery > :global(svg) { margin-block-start: 2px; }
  .recovery-copy { flex: 1 1 190px; min-inline-size: 0; }
  .recovery-copy p { margin: 3px 0 0; color: var(--text); overflow-wrap: anywhere; }
  .recovery-copy .cached { color: var(--muted); }
</style>
