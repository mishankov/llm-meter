<script lang="ts">
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
  import ArrowRightIcon from '@lucide/svelte/icons/arrow-right';
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
  import InboxIcon from '@lucide/svelte/icons/inbox';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/svelte/dialog';
  import type { UsageRequest } from './api';
  import { count, formatDateTime, formatDay, formatTime, guardEnter, money, tokens } from './format';

  interface Props {
    requests?: UsageRequest[];
    loading: boolean;
    bucketLabel: string;
    canPrevious: boolean;
    canNext: boolean;
    onPrevious: () => void;
    onNext: () => void;
  }

  let { requests, loading, bucketLabel, canPrevious, canNext, onPrevious, onNext }: Props = $props();
  let selected = $state<UsageRequest | null>(null);
  let open = $state(false);

  function showDetail(request: UsageRequest) {
    selected = request;
    open = true;
  }

  function status(request: UsageRequest) {
    return request.status ? String(request.status) : 'Interrupted';
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
    return 'Estimated usage';
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

<section class="requests-section" id="requests" aria-labelledby="requests-title" aria-busy={loading}>
  <header class="section-head">
    <div>
      <h2 id="requests-title" tabindex="-1">Recent requests</h2>
      {#if bucketLabel}<p class="request-window muted">{bucketLabel}</p>{/if}
    </div>
    {#if canPrevious || canNext}
      <div class="pagination">
        <Button class="icon-button" aria-label="Previous requests" disabled={!canPrevious || loading} onclick={onPrevious}>
          <ArrowLeftIcon size={15} aria-hidden="true" />
        </Button>
        <Button class="icon-button" aria-label="Next requests" disabled={!canNext || loading} onclick={onNext}>
          <ArrowRightIcon size={15} aria-hidden="true" />
        </Button>
      </div>
    {/if}
  </header>
  {#if requests?.length}
    <div class="table-scroll">
      <table>
        <thead>
          <tr>
            <th scope="col">Time<span class="compact-only"> / Status</span></th><th scope="col" class="wide-only">Client</th><th scope="col">Model</th>
            <th scope="col" class="wide-only">Endpoint</th><th scope="col" class="wide-only">Status</th>
            <th scope="col" class="numeric wide-only">Tokens</th><th scope="col" class="numeric">Usage (USD)</th><th scope="col" class="details-column"><span class="sr-only">Details</span></th>
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
                <span class="compact-only compact-status status-code" class:bad={request.status === 0 || request.status >= 400}>{status(request)}</span>
              </td>
              <td class="wide-only client-cell">{@render attribution(request)}</td>
              <td class="model-cell">
                <span class="clip" title={request.model}>{request.model || '—'}</span>
                <div class="compact-only">{@render attribution(request)}</div>
              </td>
              <td class="endpoint-cell wide-only" title={request.method + ' ' + request.endpoint}>{request.method} {request.endpoint}</td>
              <td class="wide-only"><span class="status-code" class:bad={request.status === 0 || request.status >= 400}>{status(request)}</span></td>
              <td class="numeric wide-only">{request.usageAvailable ? count(tokens(request)) : '—'}</td>
              <td class="numeric amount-cell" class:muted={request.cost === null}>
                {amount(request)}
                {#if request.cost !== null}
                  <span class="amount-method muted">{basis === 'Estimated usage' ? 'Estimated' : basis}</span>
                {/if}
              </td>
              <td>
                <Button class="icon-button row-details" title="Request details (Enter)" aria-label={'Details for ' + (request.model || request.endpoint) + ' at ' + formatDateTime(request.at)} onkeydown={guardEnter} onclick={() => showDetail(request)}>
                  <ChevronRightIcon size={15} aria-hidden="true" />
                </Button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <div class="empty-state">
      <InboxIcon size={21} aria-hidden="true" />
      <p>{loading ? 'Loading requests…' : requests ? bucketLabel ? 'No requests in this time window.' : 'No requests in this view.' : 'Request results unavailable.'}</p>
    </div>
  {/if}
</section>

<Dialog bind:open>
  <DialogBackdrop class="dialog-backdrop" />
  <DialogContent class="dialog-content request-detail" initialFocus="content">
    {#if selected}
      <DialogClose class="icon-button dialog-x" aria-label="Close request details (Escape)">
        <XIcon size={19} aria-hidden="true" />
      </DialogClose>
      <DialogTitle class="dialog-title">Request details</DialogTitle>
      <DialogDescription class="muted">{selected.method} {selected.endpoint}</DialogDescription>
      <dl class="detail-list">
        <div><dt>Time</dt><dd>{formatDateTime(selected.at)}</dd></div>
        <div><dt>Source</dt><dd>{sourceLabel(selected)}</dd></div>
        <div><dt>Client</dt><dd>{selected.client || '—'}</dd></div>
        <div><dt>Model</dt><dd>{selected.model || '—'}</dd></div>
        <div><dt>Status</dt><dd>{status(selected)}</dd></div>
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
  .request-window { margin: 4px 0 0; }
  .pagination { display: flex; gap: 5px; }
  th, td { white-space: nowrap; }
  td { vertical-align: top; }
  .clip, .endpoint-cell, .client-cell { max-inline-size: 168px; overflow: hidden; text-overflow: ellipsis; }
  .clip, .request-client { display: block; overflow: hidden; text-overflow: ellipsis; }
  .endpoint-cell { font-family: var(--mono); color: var(--muted); font-size: 12px; }
  .request-source, .amount-method { display: block; margin-block-start: 3px; font-size: 12px; }
  .request-source { color: var(--api-light); }
  .request-source.codex { color: var(--codex-light); }
  .status-code { font-family: var(--mono); color: var(--success); font-size: 12px; }
  .status-code.bad { color: var(--danger); }
  td:last-child { vertical-align: middle; }
  :global(.row-details) { inline-size: 32px; block-size: 32px; }
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
    table { table-layout: fixed; }
    th, td { white-space: normal; }
    .wide-only { display: none; }
    .compact-only { display: inline; }
    th:first-child { inline-size: 25%; }
    .amount-cell, .model-cell { overflow-wrap: anywhere; }
    .compact-time, .compact-status, div.compact-only { display: block; }
    .compact-status { margin-block-start: 3px; }
    .details-column { inline-size: 48px; }
    :global(.row-details) { inline-size: 40px; block-size: 40px; }
  }
</style>
