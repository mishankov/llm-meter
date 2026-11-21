<script lang="ts">
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
  import ArrowRightIcon from '@lucide/svelte/icons/arrow-right';
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
  import InboxIcon from '@lucide/svelte/icons/inbox';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/svelte/dialog';
  import type { UsageRequest } from './api';
  import { count, formatDateTime, guardEnter, money, tokens } from './format';

  interface Props {
    requests?: UsageRequest[];
    loading: boolean;
    canPrevious: boolean;
    canNext: boolean;
    onPrevious: () => void;
    onNext: () => void;
  }

  let { requests, loading, canPrevious, canNext, onPrevious, onNext }: Props = $props();
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
    if (request.priceSource === 'provider-reported') return 'Reported cost';
    if (request.priceSource === 'provider-list') return 'Provider list value';
    return 'Estimated usage';
  }

  const rate = (n: number | null | undefined) => n == null ? '—' : money(n);
</script>

<section class="requests-section" id="requests" aria-labelledby="requests-title" aria-busy={loading}>
  <header class="section-head">
    <h2 id="requests-title">Recent requests</h2>
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
            <th scope="col">Time</th><th scope="col" class="wide-only">Client</th><th scope="col">Model</th>
            <th scope="col" class="wide-only">Endpoint</th><th scope="col">Status</th>
            <th scope="col" class="numeric wide-only">Tokens</th><th scope="col" class="numeric">Est. USD</th><th scope="col"><span class="sr-only">Details</span></th>
          </tr>
        </thead>
        <tbody>
          {#each requests as request (request.id)}
            <tr>
              <td class="muted numeric"><time datetime={request.at}>{formatDateTime(request.at)}</time></td>
              <td class="wide-only">
                {#if request.client}{request.client}{:else}<span class="muted">—</span>{/if}
                <span class="request-source" class:codex={request.source === 'codex'}>{request.source === 'codex' ? 'Codex' : 'API'}</span>
              </td>
              <td class="clip" title={request.model}>{request.model || '—'}</td>
              <td class="endpoint-cell wide-only" title={request.method + ' ' + request.endpoint}>{request.method} {request.endpoint}</td>
              <td><span class="status-code" class:bad={request.status === 0 || request.status >= 400}>{status(request)}</span></td>
              <td class="numeric wide-only">{request.usageAvailable ? count(tokens(request)) : '—'}</td>
              <td class="numeric" class:muted={request.cost === null}>{amount(request)}</td>
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
      <p>{loading ? 'Loading requests…' : requests ? 'No requests in this view.' : 'Request results unavailable.'}</p>
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
        <div><dt>Source</dt><dd>{selected.source === 'codex' ? 'Codex subscription' : selected.provider === 'opencode-go' ? 'OpenCode Go' : 'API'}</dd></div>
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
  .pagination { display: flex; gap: 5px; }
  th, td { white-space: nowrap; }
  .clip, .endpoint-cell { max-inline-size: 168px; overflow: hidden; text-overflow: ellipsis; }
  .endpoint-cell { font-family: var(--mono); color: var(--muted); font-size: 11px; }
  .request-source { color: var(--api-light); font-size: 10px; display: block; margin-top: 2px; }
  .request-source.codex { color: var(--codex-light); }
  .status-code { font-family: var(--mono); color: var(--success); font-size: 11px; }
  .status-code.bad { color: var(--danger); }
  :global(.row-details) { inline-size: 26px; block-size: 26px; }
  :global(.dialog-content.request-detail) { margin: 0 0 0 auto; max-block-size: 100dvh; block-size: 100dvh; max-inline-size: min(100%, 430px); border-radius: 0; }
  .detail-list { margin-block: 22px; }
  .detail-list div { padding: 10px 0; border-bottom: 1px solid var(--line); display: grid; gap: 10px; grid-template-columns: 130px minmax(0, 1fr); }
  .detail-list dt { color: var(--muted); }
  .detail-list dd { margin: 0; overflow-wrap: anywhere; }
  .details-heading { font-size: 14px; margin: 22px 0 12px; }
  .detail-rates { display: grid; grid-template-columns: 1fr auto; gap: 8px 12px; color: var(--muted); }
  .detail-rates.with-rates { grid-template-columns: 1fr auto auto; }
  .detail-rates b { font-weight: 500; color: var(--text); text-align: end; font-variant-numeric: tabular-nums; }
  @media (max-width: 750px) { .wide-only { display: none; } }
  @media (max-width: 540px) { .clip { max-inline-size: 92px; } }
</style>
