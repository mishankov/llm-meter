<script lang="ts">
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
  import ArrowRightIcon from '@lucide/svelte/icons/arrow-right';
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
  import InboxIcon from '@lucide/svelte/icons/inbox';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/svelte/dialog';
  import type { DashboardProblem, RequestOutcome, UsageRequest } from './api';
  import Filter from './Filter.svelte';
  import { count, formatDateTime, formatDay, formatTime, guardEnter, money, requestStatusGuidance, requestStatusLabel, tokens } from './format';
  import RecoveryNotice from './RecoveryNotice.svelte';

  interface Props {
    requests?: UsageRequest[];
    loading: boolean;
    bucketLabel: string;
    outcome: RequestOutcome;
    onOutcomeChange: (outcome: RequestOutcome) => void;
    pageNumber: number;
    error: DashboardProblem | null;
    retry: () => void;
    canPrevious: boolean;
    canNext: boolean;
    onPrevious: () => void;
    onNext: () => void;
  }

  let { requests, loading, bucketLabel, outcome, onOutcomeChange, pageNumber, error, retry, canPrevious, canNext, onPrevious, onNext }: Props = $props();
  let selected = $state<UsageRequest | null>(null);
  let open = $state(false);
  let heading: HTMLHeadingElement;
  let outcomeFilter: HTMLDivElement;
  let detailTrigger: HTMLButtonElement | null = null;

  const outcomeOptions = [
    { value: '', label: 'All outcomes' },
    { value: 'succeeded', label: 'HTTP 2xx responses' },
    { value: 'failed', label: 'HTTP errors or interrupted' }
  ];

  const emptyMessage = $derived(outcome === 'failed'
    ? 'No HTTP errors or interrupted requests in this view.'
    : outcome === 'succeeded'
      ? 'No HTTP 2xx responses in this view.'
      : bucketLabel ? 'No requests in this time window.' : 'No requests in this view.');

  function changeOutcome(value: string) {
    if (value === '' || value === 'succeeded' || value === 'failed') onOutcomeChange(value);
  }

  function showAllOutcomes() {
    outcomeFilter.querySelector<HTMLElement>('[role="combobox"]')?.focus();
    onOutcomeChange('');
  }

  function retryRequests() {
    heading.focus({ preventScroll: true });
    retry();
  }

  function showDetail(request: UsageRequest, event: MouseEvent) {
    detailTrigger = event.currentTarget as HTMLButtonElement;
    selected = request;
    open = true;
  }

  function amount(request: UsageRequest) {
    if (request.cost !== null) return money(request.cost);
    if (!request.usageAvailable) return 'Usage unavailable';
    return 'Unpriced';
  }

  function costLabel(request: UsageRequest) {
    if (request.cost === null) return 'Usage value';
    if (request.priceSource === 'provider-reported') return 'Reported cost';
    if (request.priceSource === 'provider-list') return 'Provider list value';
    if (request.priceSource === 'provider-estimate') return 'Provider estimate';
    return 'Estimated cost';
  }

  const providerLabels: Record<string, string> = {
    openai: 'OpenAI API',
    openrouter: 'OpenRouter',
    'opencode-go': 'OpenCode Go',
    opencode: 'OpenCode',
    anthropic: 'Anthropic',
    deepseek: 'DeepSeek',
    groq: 'Groq',
    mistral: 'Mistral',
    xai: 'xAI',
    google: 'Google'
  };

  function sourceLabel(request: UsageRequest) {
    if (request.source === 'codex') return 'Codex subscription';
    return providerLabels[request.provider ?? ''] ?? 'API';
  }

  const rate = (n: number | null | undefined) => n == null ? '—' : money(n);
</script>

{#snippet attribution(request: UsageRequest)}
  {#if request.client}<span class="request-client" title={request.client}>{request.client}</span>{/if}
  <span class="request-source" class:codex={request.source === 'codex'}>{sourceLabel(request)}</span>
{/snippet}

{#snippet requestStatus(request: UsageRequest)}
  {@const label = requestStatusLabel(request.status)}
  <span class="status-code" class:bad={request.status === 0 || request.status >= 400} title={label}>
    {#if request.status}
      <span class="status-number" aria-hidden="true">{request.status}</span><span class="sr-only">HTTP {request.status}, {label}</span>
      {#if request.status === 101 || request.status >= 400}
        <span class="status-label" aria-hidden="true">{label}</span>
      {/if}
    {:else}
      {label}
    {/if}
  </span>
{/snippet}

<section class="requests-section" id="requests" aria-labelledby="requests-title" aria-busy={loading}>
  <header class="section-head request-heading">
    <div>
      <h2 id="requests-title" bind:this={heading} tabindex="-1">Requests</h2>
      {#if bucketLabel || requests?.length}
        <p class="request-summary muted">
          {#if bucketLabel}<span>{bucketLabel}</span>{/if}
          {#if bucketLabel && requests?.length}<span aria-hidden="true">·</span>{/if}
          {#if requests?.length}
            <span>{count(requests.length)} {requests.length === 1 ? 'request' : 'requests'} on this page</span>
          {/if}
        </p>
      {/if}
    </div>
    <div class="request-controls">
      <div class="outcome-filter" bind:this={outcomeFilter}>
        <Filter label="Request outcome" value={outcome} items={outcomeOptions} onChange={changeOutcome} />
      </div>
      <div class="pagination" role="group" aria-label="Request pages">
        <Button class="icon-button" aria-label="Previous requests" title="Previous requests" disabled={!canPrevious || loading}
          focusableWhenDisabled={loading || canPrevious || canNext} onclick={onPrevious} onkeydown={guardEnter}>
          <ArrowLeftIcon size={15} aria-hidden="true" />
        </Button>
        <span class="page-number muted" role="status">Page {count(pageNumber)}</span>
        <Button class="icon-button" aria-label="Next requests" title="Next requests" disabled={!canNext || loading}
          focusableWhenDisabled={loading || canPrevious || canNext} onclick={onNext} onkeydown={guardEnter}>
          <ArrowRightIcon size={15} aria-hidden="true" />
        </Button>
      </div>
    </div>
  </header>
  {#if error}
    <div class="request-error">
      <RecoveryNotice problem={error} retry={retryRequests} busy={loading} cached={Boolean(requests?.length)}
        cachedMessage="Showing the last loaded requests for this page." retryLabel="Retry requests" />
    </div>
  {/if}
  {#if requests?.length}
    <div class="table-scroll">
      <table>
        <thead>
          <tr>
            <th scope="col">Time<span class="compact-only"> / Status</span></th><th scope="col">Model</th><th scope="col" class="wide-only">Client</th>
            <th scope="col" class="wide-only">Status</th>
            <th scope="col" class="numeric wide-only">Tokens</th><th scope="col" class="numeric">Usage value (USD)</th><th scope="col" class="details-column"><span class="sr-only">Request actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each requests as request (request.id)}
            {@const basis = costLabel(request)}
            <tr>
              <td class="muted numeric">
                <time datetime={request.at}>
                  <span class="wide-only">{formatDateTime(request.at)}</span>
                  <span class="compact-only compact-time">{formatDay(request.at)}<br />{formatTime(request.at)}</span>
                </time>
                <span class="compact-only compact-status">{@render requestStatus(request)}</span>
              </td>
              <td class="model-cell">
                <span class="clip" title={request.model}>{request.model || '—'}</span>
                <div class="compact-only">{@render attribution(request)}</div>
              </td>
              <td class="wide-only client-cell">{@render attribution(request)}</td>
              <td class="wide-only">{@render requestStatus(request)}</td>
              <td class="numeric wide-only">{request.usageAvailable ? count(tokens(request)) : '—'}</td>
              <td class="numeric amount-cell" class:muted={request.cost === null}>
                {amount(request)}
                {#if request.cost !== null}
                  <span class="amount-method muted">{basis}</span>
                {/if}
              </td>
              <td>
                <Button class="secondary-button row-details" title="Request details (Enter)"
                  aria-label={'Details for ' + (request.model || request.endpoint) + ' at ' + formatDateTime(request.at)}
                  aria-keyshortcuts="Enter" onkeydown={guardEnter} onclick={(event) => showDetail(request, event)}>
                  Details <ChevronRightIcon size={14} aria-hidden="true" />
                </Button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if !error}
    <div class="empty-state">
      <InboxIcon size={21} aria-hidden="true" />
      {#if loading}
        <p role="status">Loading requests…</p>
      {:else}
        <p>{requests ? emptyMessage : 'Request results unavailable.'}</p>
        {#if requests && outcome}
          <Button class="secondary-button" title="Show all outcomes (Enter)" aria-keyshortcuts="Enter"
            onclick={showAllOutcomes} onkeydown={guardEnter}>Show all outcomes</Button>
        {/if}
      {/if}
    </div>
  {/if}
</section>

<Dialog bind:open>
  <DialogBackdrop class="dialog-backdrop" />
  <DialogContent class="dialog-content request-detail" initialFocus="content" finalFocus={() => detailTrigger?.isConnected ? detailTrigger : heading}>
    {#if selected}
      <DialogClose class="icon-button dialog-x" aria-label="Close request details" title="Close request details (Escape)" aria-keyshortcuts="Escape">
        <XIcon size={19} aria-hidden="true" />
      </DialogClose>
      <DialogTitle class="dialog-title">Request details</DialogTitle>
      <DialogDescription class="muted">{selected.method} {selected.endpoint}</DialogDescription>
      <dl class="detail-list">
        <div><dt>Time</dt><dd>{formatDateTime(selected.at)}</dd></div>
        <div><dt>Source</dt><dd>{sourceLabel(selected)}</dd></div>
        <div><dt>Client</dt><dd>{selected.client || '—'}</dd></div>
        <div><dt>Model</dt><dd>{selected.model || '—'}</dd></div>
        <div>
          <dt>Status</dt>
          <dd>{selected.status ? `${requestStatusLabel(selected.status)} · HTTP ${selected.status}` : requestStatusLabel(selected.status)}</dd>
        </div>
        {#if selected.status === 0 || selected.status >= 400}
          <div><dt>Next step</dt><dd>{requestStatusGuidance(selected.status)}</dd></div>
        {/if}
        <div><dt>{costLabel(selected)}</dt><dd>{amount(selected)}</dd></div>
        {#if selected.reportedListCost != null && selected.priceSource !== 'provider-list'}
          <div><dt>Provider list value</dt><dd>{money(selected.reportedListCost)}</dd></div>
        {/if}
        {#if selected.reportedAllowance != null}
          <div><dt>Allowance used</dt><dd>{money(selected.reportedAllowance)}</dd></div>
        {/if}
      </dl>
      {#if selected.usageAvailable}
        <h3 class="details-heading">{selected.rates ? 'Usage and rates' : 'Token usage'}</h3>
        <div class="detail-rates" class:with-rates={selected.rates}>
          <span></span><span>Tokens</span>{#if selected.rates}<span>USD / 1M</span>{/if}
          <span>Uncached input</span><b>{count(Math.max(0, selected.inputTokens - selected.cachedInputTokens - (selected.cacheWriteTokens ?? 0)))}</b>{#if selected.rates}<b>{rate(selected.rates.input)}</b>{/if}
          <span>Cached input</span><b>{count(selected.cachedInputTokens)}</b>{#if selected.rates}<b>{rate(selected.rates.cachedInput)}</b>{/if}
          {#if (selected.cacheWriteTokens ?? 0) > (selected.cacheWrite12hTokens ?? 0)}
            <span>30-minute cache writes</span><b>{count((selected.cacheWriteTokens ?? 0) - (selected.cacheWrite12hTokens ?? 0))}</b>{#if selected.rates}<b>{rate(selected.rates.cacheWrite)}</b>{/if}
          {/if}
          {#if selected.cacheWrite12hTokens}
            <span>12-hour cache writes</span><b>{count(selected.cacheWrite12hTokens)}</b>{#if selected.rates}<b>{rate(selected.rates.cacheWrite12h)}</b>{/if}
          {/if}
          <span>Output</span><b>{count(selected.outputTokens)}</b>{#if selected.rates}<b>{rate(selected.rates.output)}</b>{/if}
        </div>
      {/if}
    {/if}
  </DialogContent>
</Dialog>

<style>
  .requests-section { border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  #requests-title { scroll-margin-block-start: 75px; }
  .request-heading, .request-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
  .request-controls { justify-content: space-between; }
  .request-summary { display: flex; flex-wrap: wrap; gap: 0 6px; margin: 4px 0 0; }
  .pagination { display: flex; align-items: center; gap: 5px; }
  .page-number { min-inline-size: 5ch; text-align: center; font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .request-error { margin-block-end: var(--gap); }
  th, td { white-space: nowrap; }
  td { vertical-align: top; }
  .clip, .client-cell { max-inline-size: 168px; overflow: hidden; text-overflow: ellipsis; }
  .clip, .request-client { display: block; overflow: hidden; text-overflow: ellipsis; }
  .request-source, .amount-method { display: block; margin-block-start: 3px; font-size: 12px; }
  .request-source { color: var(--api-light); }
  .request-source.codex { color: var(--codex-light); }
  .status-code { color: var(--success); font-size: 12px; }
  .status-code.bad { color: var(--danger); }
  .status-number { font-family: var(--mono); }
  .status-label { display: block; margin-block-start: 3px; color: var(--muted); white-space: normal; }
  td:last-child { vertical-align: middle; }
  :global(.dialog-content.request-detail) { margin: 0 0 0 auto; max-block-size: 100dvh; block-size: 100dvh; max-inline-size: min(100%, 430px); border-radius: 0; }
  .detail-list { margin-block: 22px; }
  .detail-list div { padding: 10px 0; border-bottom: 1px solid var(--line); display: grid; gap: 10px; grid-template-columns: 130px minmax(0, 1fr); }
  .detail-list dt { color: var(--muted); }
  .detail-list dd { margin: 0; overflow-wrap: anywhere; }
  .details-heading { font-size: 14px; margin: 22px 0 12px; }
  .detail-rates { display: grid; grid-template-columns: 1fr auto; gap: 8px 12px; color: var(--muted); }
  .detail-rates.with-rates { grid-template-columns: 1fr auto auto; }
  .detail-rates b { font-weight: 500; color: var(--text); text-align: end; font-variant-numeric: tabular-nums; }
  .compact-only { display: none; }
  @media (max-width: 750px) {
    .request-controls { inline-size: 100%; }
    .request-controls :global(.icon-button) { inline-size: 44px; block-size: 44px; }
    .outcome-filter :global(.filter-trigger) { min-block-size: 44px; }
    table { table-layout: fixed; }
    th, td { white-space: normal; }
    .wide-only { display: none; }
    .compact-only { display: inline; }
    th:first-child { inline-size: 25%; }
    .amount-cell, .model-cell { overflow-wrap: anywhere; }
    .compact-time, .compact-status, div.compact-only { display: block; }
    .compact-status { margin-block-start: 3px; overflow-wrap: anywhere; }
    .details-column { inline-size: 68px; }
    :global(.row-details) { min-block-size: 44px; min-inline-size: 54px; padding-inline: 6px; }
    :global(.row-details > svg) { display: none; }
  }
</style>
