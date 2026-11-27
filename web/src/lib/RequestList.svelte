<script lang="ts">
  import { tick } from 'svelte';
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
  import ArrowRightIcon from '@lucide/svelte/icons/arrow-right';
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
  import InboxIcon from '@lucide/svelte/icons/inbox';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/svelte/dialog';
  import type { DashboardProblem, RequestOutcome, UsageRequest } from './api';
  import Filter from './Filter.svelte';
  import { compactCount, compactMoney, count, formatDateTimeSeconds, formatDay, formatTimeSeconds, guardEnter, money, requestStatusGuidance, requestStatusLabel, tokens, uncachedInput } from './format';
  import RecoveryNotice from './RecoveryNotice.svelte';

  interface Props {
    requests?: UsageRequest[];
    loading: boolean;
    bucket: string;
    bucketLabel: string;
    windowOptions: { value: string; label: string }[];
    onBucketChange: (bucket: string) => void;
    onClearBucket: () => void;
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

  let {
    requests, loading, bucket, bucketLabel, windowOptions, onBucketChange, onClearBucket,
    outcome, onOutcomeChange, pageNumber, error, retry, canPrevious, canNext, onPrevious, onNext
  }: Props = $props();
  let selected = $state<UsageRequest | null>(null);
  let open = $state(false);
  let expanded = $state(false);
  let previousPage = 1;
  const previewSize = 12;
  const wholePercent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 0 });
  let remaining = $derived(Math.max(0, (requests?.length ?? 0) - previewSize));
  let shownRequests = $derived(expanded ? requests : requests?.slice(0, previewSize));
  let previewCommand = $derived(expanded ? 'Show fewer requests' : 'Show ' + count(remaining) + ' more ' + (remaining === 1 ? 'request' : 'requests'));
  let heading: HTMLHeadingElement;
  let requestTable = $state<HTMLDivElement>();
  let outcomeFilter: HTMLDivElement;
  let detailTrigger: HTMLButtonElement | null = null;

  $effect(() => {
    if (pageNumber !== previousPage) {
      previousPage = pageNumber;
      expanded = false;
    }
  });

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

  async function togglePreview() {
    expanded = !expanded;
    if (expanded) {
      await tick();
      requestTable?.querySelectorAll<HTMLButtonElement>('.row-details')[previewSize]?.focus();
    }
  }

  function isEstimatedCost(request: UsageRequest) {
    return request.cost !== null &&
      request.priceSource !== 'provider-reported' &&
      request.priceSource !== 'provider-list' &&
      request.priceSource !== 'provider-estimate';
  }

  function amount(request: UsageRequest) {
    if (request.cost !== null) return money(request.cost);
    if (!request.usageAvailable) return 'Usage unavailable';
    return 'Unpriced';
  }

  function tableAmount(request: UsageRequest) {
    if (request.cost === null) return amount(request);
    const formatted = compactMoney(request.cost);
    return isEstimatedCost(request) ? '~' + formatted : formatted;
  }

  function tableAmountTitle(request: UsageRequest) {
    if (request.cost === null) return undefined;
    return `${costLabel(request)}: ${money(request.cost)}`;
  }

  function costLabel(request: UsageRequest) {
    if (request.cost === null) return 'Usage value';
    if (request.priceSource === 'provider-reported') return 'Reported cost';
    if (request.priceSource === 'provider-list') return 'Provider list value';
    if (request.priceSource === 'provider-estimate') return 'Provider estimate';
    return 'Estimated cost';
  }

  function tokenSummary(request: UsageRequest) {
    if (!request.usageAvailable || tokens(request) <= 0) return '';
    const parts = [
      `${compactCount(request.inputTokens)} in`,
      `${compactCount(request.outputTokens)} out`
    ];
    if (request.inputTokens > 0 && request.cachedInputTokens > 0) {
      parts.push(`${wholePercent.format(request.cachedInputTokens / request.inputTokens)} cache`);
    }
    return parts.join(' · ');
  }

  const providerLabels: Record<string, string> = {
    antigravity: 'Antigravity',
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

  function isTelemetryEndpoint(endpoint: string) {
    return endpoint === '/claude-code/api-request' ||
      endpoint === '/codex/telemetry' ||
      endpoint === '/gemini-cli/api-request' ||
      endpoint === '/copilot/chat' ||
      endpoint === '/opencode/telemetry';
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
    <div class="request-title-group">
      <h2 id="requests-title" bind:this={heading} tabindex="-1">Requests</h2>
      {#if bucketLabel || requests?.length}
        <div class="request-summary muted">
          {#if bucketLabel}
            <span class="window-chip" aria-live="polite">
              <span>{bucketLabel}</span>
              <Button
                class="icon-button window-chip-clear"
                title="Clear chart time filter (Enter)"
                aria-label="Clear chart time filter"
                aria-keyshortcuts="Enter"
                onclick={onClearBucket}
                onkeydown={guardEnter}
              >
                <XIcon size={13} aria-hidden="true" />
              </Button>
            </span>
          {/if}
          {#if requests?.length}
            <span>{remaining && !expanded ? 'Showing ' + previewSize + ' of ' : ''}{count(requests.length)} {requests.length === 1 ? 'request' : 'requests'} on this page</span>
          {/if}
        </div>
      {/if}
    </div>
    <div class="request-controls">
      <div class="window-filter" id="request-window-filter">
        <Filter label="Request time window" value={bucket} items={windowOptions} onChange={onBucketChange} />
      </div>
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
    <div class="table-scroll" id="request-table" bind:this={requestTable}>
      <table>
        <thead>
          <tr>
            <th scope="col">Time<span class="compact-only"> / Status</span></th><th scope="col">Model</th><th scope="col" class="wide-only">Reasoning</th><th scope="col" class="wide-only">Client</th>
            <th scope="col" class="wide-only">Status</th>
            <th scope="col" class="numeric wide-only">Tokens</th><th scope="col" class="numeric">Usage value (USD)</th><th scope="col" class="details-column"><span class="sr-only">Request actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each shownRequests ?? [] as request (request.id)}
            {@const basis = costLabel(request)}
            {@const estimated = isEstimatedCost(request)}
            {@const tokSummary = tokenSummary(request)}
            <tr>
              <td class="muted numeric">
                <time datetime={request.at}>
                  <span class="wide-only">{formatDateTimeSeconds(request.at)}</span>
                  <span class="compact-only compact-time">{formatDay(request.at)}<br />{formatTimeSeconds(request.at)}</span>
                </time>
                <span class="compact-only compact-status">{@render requestStatus(request)}</span>
              </td>
              <td class="model-cell">
                <div class="model-line">
                  <span class="clip model-name" title={request.model}>{request.model || '—'}</span>
                  <span class="compact-only compact-reasoning">
                    {#if request.reasoningLevel}
                      <span class="reasoning-pill" data-level={request.reasoningLevel}>{request.reasoningLevel}</span>
                    {:else}
                      <span class="reasoning-pill" data-level="unreported" aria-hidden="true">—</span><span class="sr-only">Not reported</span>
                    {/if}
                  </span>
                </div>
                <div class="compact-only">{@render attribution(request)}</div>
              </td>
              <td class="wide-only reasoning-cell">
                {#if request.reasoningLevel}
                  <span class="reasoning-pill" data-level={request.reasoningLevel}>{request.reasoningLevel}</span>
                {:else}
                  <span class="reasoning-pill" data-level="unreported">Not reported</span>
                {/if}
              </td>
              <td class="wide-only client-cell">{@render attribution(request)}</td>
              <td class="wide-only">{@render requestStatus(request)}</td>
              <td class="numeric wide-only tokens-cell">
                {#if request.usageAvailable}
                  <span>{count(tokens(request))}</span>
                  {#if tokSummary}<span class="token-sub muted">{tokSummary}</span>{/if}
                {:else}
                  <span class="muted">—</span>
                {/if}
              </td>
              <td class="numeric amount-cell" class:muted={request.cost === null}>
                <span title={tableAmountTitle(request)} aria-label={estimated && request.cost !== null ? `Estimated ${money(request.cost)}` : undefined}>{tableAmount(request)}</span>
                {#if request.cost !== null && !estimated}
                  <span class="amount-badge">{basis}</span>
                {/if}
              </td>
              <td>
                <Button class="secondary-button row-details" title="Request details (Enter)"
                  aria-label={'Details for ' + (request.model || request.endpoint) + ' at ' + formatDateTimeSeconds(request.at)}
                  aria-keyshortcuts="Enter" onkeydown={guardEnter} onclick={(event) => showDetail(request, event)}>
                  Details <ChevronRightIcon size={14} aria-hidden="true" />
                </Button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if remaining}
      <div class="request-disclosure">
        <Button class="secondary-button" title={previewCommand + ' (Enter or Space)'} aria-keyshortcuts="Enter Space"
          aria-expanded={expanded} aria-controls="request-table" disabled={loading || open} focusableWhenDisabled
          onkeydown={guardEnter} onclick={togglePreview}>{previewCommand}</Button>
      </div>
    {/if}
  {:else if !error}
    <div class="empty-state">
      <InboxIcon size={21} aria-hidden="true" />
      {#if loading}
        <p role="status">Loading requests…</p>
      {:else}
        <p>{requests ? emptyMessage : 'Request results unavailable.'}</p>
        {#if requests && (outcome || bucket)}
          <div class="empty-actions">
            {#if outcome}
              <Button class="secondary-button" title="Show all outcomes (Enter)" aria-keyshortcuts="Enter"
                onclick={showAllOutcomes} onkeydown={guardEnter}>Show all outcomes</Button>
            {/if}
            {#if bucket}
              <Button class="secondary-button" title="Show all time windows (Enter)" aria-keyshortcuts="Enter"
                onclick={onClearBucket} onkeydown={guardEnter}>Show all time windows</Button>
            {/if}
          </div>
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
        <div><dt>Time</dt><dd>{formatDateTimeSeconds(selected.at)}</dd></div>
        <div><dt>Source</dt><dd>{sourceLabel(selected)}</dd></div>
        <div><dt>Client</dt><dd>{selected.client || '—'}</dd></div>
        <div><dt>Model</dt><dd class="detail-model">{selected.model || '—'}</dd></div>
        <div>
          <dt>Reasoning</dt>
          <dd>
            {#if selected.reasoningLevel}
              <span class="reasoning-pill" data-level={selected.reasoningLevel}>{selected.reasoningLevel}</span>
            {:else}
              <span class="muted">— · Not reported{isTelemetryEndpoint(selected.endpoint) ? ' (OpenTelemetry)' : ''}</span>
            {/if}
          </dd>
        </div>
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
        <table class="detail-rates-table">
          <thead>
            <tr>
              <th scope="col"><span class="sr-only">Category</span></th>
              <th scope="col" class="numeric">Tokens</th>
              {#if selected.rates}<th scope="col" class="numeric">USD / 1M</th>{/if}
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Uncached input</th>
              <td class="numeric">{count(uncachedInput(selected))}</td>
              {#if selected.rates}<td class="numeric">{rate(selected.rates.input)}</td>{/if}
            </tr>
            <tr>
              <th scope="row">Cached input</th>
              <td class="numeric">{count(selected.cachedInputTokens)}</td>
              {#if selected.rates}<td class="numeric">{rate(selected.rates.cachedInput)}</td>{/if}
            </tr>
            {#if (selected.cacheWriteTokens ?? 0) > (selected.cacheWrite12hTokens ?? 0)}
              <tr>
                <th scope="row">30-minute cache writes</th>
                <td class="numeric">{count((selected.cacheWriteTokens ?? 0) - (selected.cacheWrite12hTokens ?? 0))}</td>
                {#if selected.rates}<td class="numeric">{rate(selected.rates.cacheWrite)}</td>{/if}
              </tr>
            {/if}
            {#if selected.cacheWrite12hTokens}
              <tr>
                <th scope="row">12-hour cache writes</th>
                <td class="numeric">{count(selected.cacheWrite12hTokens)}</td>
                {#if selected.rates}<td class="numeric">{rate(selected.rates.cacheWrite12h)}</td>{/if}
              </tr>
            {/if}
            <tr>
              <th scope="row">Output</th>
              <td class="numeric">{count(selected.outputTokens)}</td>
              {#if selected.rates}<td class="numeric">{rate(selected.rates.output)}</td>{/if}
            </tr>
            <tr class="detail-rates-total">
              <th scope="row">Total tokens</th>
              <td class="numeric">{count(tokens(selected))}</td>
              {#if selected.rates}<td class="numeric muted">—</td>{/if}
            </tr>
          </tbody>
        </table>
      {/if}
    {/if}
  </DialogContent>
</Dialog>

<style>
  .request-disclosure { margin-block-start: 10px; }
  .requests-section { border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  #requests-title { scroll-margin-block-start: 8px; }
  .request-heading, .request-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
  .request-title-group { min-inline-size: 0; }
  .request-controls { justify-content: flex-end; gap: 8px; }
  .window-filter :global(.filter-trigger) { min-inline-size: 145px; }
  .request-summary { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; margin: 5px 0 0; font-size: 12px; }
  .window-chip { display: inline-flex; align-items: center; gap: 4px; padding: 1px 2px 1px 8px; border-radius: 5px; border: 1px solid var(--selected-border); background: var(--nav-current); color: var(--nav-current-text); font-size: 11px; font-weight: 550; }
  .window-chip :global(.window-chip-clear) { inline-size: 24px; block-size: 24px; border-color: transparent; background: transparent; color: inherit; border-radius: 4px; }
  .window-chip :global(.window-chip-clear:hover) { background: var(--option-highlighted); }
  .pagination { display: flex; align-items: center; gap: 5px; }
  .page-number { min-inline-size: 5ch; text-align: center; font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .request-error { margin-block-end: var(--gap); }
  .empty-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-block-start: 4px; }
  th, td { white-space: nowrap; }
  td { vertical-align: top; }
  .clip { max-inline-size: 240px; overflow: hidden; text-overflow: ellipsis; }
  .client-cell { max-inline-size: 184px; overflow: hidden; text-overflow: ellipsis; }
  .clip, .request-client { display: block; overflow: hidden; text-overflow: ellipsis; }
  .model-name, .detail-model { font-family: var(--mono); font-size: 11.5px; }
  .model-line { display: flex; align-items: center; gap: 6px; min-inline-size: 0; }
  .request-source { display: block; margin-block-start: 3px; font-size: 12px; color: var(--api-light); }
  .request-source.codex { color: var(--codex-light); }
  .token-sub { display: block; margin-block-start: 2px; font-size: 11px; font-variant-numeric: tabular-nums; }
  .amount-badge { display: inline-block; margin-block-start: 3px; padding: 1px 5px; border-radius: 4px; border: 1px solid var(--line); background: var(--surface-raised); font-size: 10.5px; color: var(--muted); font-weight: 520; }
  .amount-cell > span:first-child { display: block; }
  .status-code { color: var(--success); font-size: 12px; }
  .status-code.bad { color: var(--danger); }
  .status-number { font-family: var(--mono); }
  .status-label { display: block; margin-block-start: 3px; color: var(--muted); white-space: normal; }
  td:last-child { vertical-align: middle; }
  :global(.dialog-content.request-detail) { margin: 0 0 0 auto; max-block-size: 100dvh; block-size: 100dvh; max-inline-size: min(100%, 430px); border-radius: 0; }
  .detail-list { margin-block: 22px; }
  .detail-list div { padding: 10px 0; border-bottom: 1px solid var(--line); display: grid; gap: 10px; grid-template-columns: minmax(0, 9rem) minmax(0, 1fr); }
  .detail-list dt { color: var(--muted); }
  .detail-list dd { margin: 0; overflow-wrap: anywhere; }
  .details-heading { font-size: 14px; margin: 22px 0 10px; }
  .detail-rates-table { inline-size: 100%; border-collapse: collapse; font-size: 12px; }
  .detail-rates-table :is(th, td) { padding: 7px 0 7px 12px; border-block-start: 1px solid var(--line); }
  .detail-rates-table thead th { border-block-start: 0; padding-block-start: 0; padding-block-end: 6px; font-size: 11px; color: var(--muted); font-weight: 550; }
  .detail-rates-table tbody th { padding-inline-start: 0; font-weight: 450; color: var(--muted); text-align: start; font-size: 12px; }
  .detail-rates-table tbody td { font-weight: 500; color: var(--text); text-align: end; font-variant-numeric: tabular-nums; }
  .detail-rates-table tr.detail-rates-total :is(th, td) { border-block-start-color: var(--strong-line); font-weight: 600; color: var(--text); }
  .detail-rates-table tbody tr:last-child :is(th, td) { border-block-end: 0; }
  .detail-rates-table tr:hover td { background: transparent; }
  .compact-only { display: none; }
  @media (max-width: 750px) {
    .request-controls { inline-size: 100%; justify-content: flex-start; }
    .window-filter :global(.filter-trigger) { min-inline-size: 0; min-block-size: 44px; }
    .request-controls :global(.icon-button) { inline-size: 44px; block-size: 44px; }
    .outcome-filter :global(.filter-trigger) { min-block-size: 44px; }
    table { table-layout: fixed; }
    th, td { white-space: normal; }
    .wide-only { display: none; }
    .compact-only { display: inline; }
    th:first-child { inline-size: 25%; }
    .amount-cell, .request-source { overflow-wrap: anywhere; }
    .model-line { display: block; }
    .model-cell :is(.clip, .request-client) { white-space: nowrap; }
    .compact-time, .compact-status, .compact-reasoning, div.compact-only { display: block; }
    .compact-status, .compact-reasoning { margin-block-start: 3px; overflow-wrap: anywhere; }
    .details-column { inline-size: 68px; }
    :global(.row-details) { min-block-size: 44px; min-inline-size: 54px; padding-inline: 6px; }
    :global(.row-details > svg) { display: none; }
  }
  @media (pointer: coarse) {
    .window-chip :global(.window-chip-clear) { inline-size: var(--touch-target); block-size: var(--touch-target); }
  }
  @media (max-width: 360px) {
    th:first-child { inline-size: 22%; }
    th:nth-last-child(2) { inline-size: 28%; }
    .detail-list div { grid-template-columns: 1fr; gap: 3px; }
  }
</style>
