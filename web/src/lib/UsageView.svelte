<script lang="ts">
  import Clock3Icon from '@lucide/svelte/icons/clock-3';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Meter, MeterIndicator, MeterTrack } from '@ui-sdk/svelte/meter';
  import { SegmentedControl, SegmentedControlItem } from '@ui-sdk/svelte/segmented-control';
  import type { Allowance, Usage, UsageGroup } from './api';
  import { compactCount, count, formatDateTime, formatTime, formatTimeWindow, formatUTCDay, guardEnter, money, partialPricing, tokens, unpricedBucket } from './format';
  import Filter from './Filter.svelte';
  import RequestList from './RequestList.svelte';
  import TimeChart from './TimeChart.svelte';

  interface Props {
    data: Usage | null;
    filterOptions?: Usage['filters'];
    requestPage: Pick<Usage, 'requests' | 'nextCursor'> | null;
    loading: boolean;
    error: string;
    updatedAt: string;
    range: string;
    source: string;
    client: string;
    model: string;
    bucket: string;
    canPrevious: boolean;
    update: (name: 'range' | 'source' | 'client' | 'model' | 'bucket', value: string) => void;
    clearFilters: () => void;
    refresh: () => void;
    onPrevious: () => void;
    onNext: () => void;
  }

  let { data, filterOptions, requestPage, loading, error, updatedAt, range, source, client, model, bucket, canPrevious, update, clearFilters, refresh, onPrevious, onNext }: Props = $props();
  let ranking = $state('models');
  let unit = $state('dollars');
  let chartMetric = $state('cost');
  let scoped = $derived(Boolean(source || client || model || bucket));
  let firstRun = $derived(data?.summary.requestCount === 0 && !data.filters.clients.length);
  let showFilters = $derived(!firstRun || scoped || range !== '24h');
  let bucketLabel = $derived(bucket ? range === '24h' ? formatTimeWindow(bucket, 1) : formatUTCDay(bucket) + ' UTC' : '');
  let groups = $derived(
    (data?.groups[ranking === 'models' ? 'models' : 'clients'] ?? [])
      .slice().sort((a, b) => groupValue(b) - groupValue(a) || a.name.localeCompare(b.name)).slice(0, 6)
  );
  let maxRank = $derived(Math.max(0, ...groups.map(groupValue)));

  const sourceOptions = [
    { value: '', label: 'All sources' },
    { value: 'api', label: 'API providers' },
    { value: 'codex', label: 'Codex subscription' }
  ];

  let clientOptions = $derived(withSelectedOption('All clients', (filterOptions?.clients ?? []).map(({ id, name }) => ({ value: id, label: name })), client, 'Unknown client'));
  let modelOptions = $derived(withSelectedOption('All models', (filterOptions?.models ?? []).map((name) => ({ value: name, label: name })), model, model));
  let windowOptions = $derived(withSelectedOption('All time windows', (data?.buckets ?? []).map(({ start }) => ({
    value: start, label: range === '24h' ? formatDateTime(start) : formatUTCDay(start) + ' UTC'
  })), bucket, bucketLabel));

  function withSelectedOption(all: string, options: { value: string; label: string }[], selected: string, fallback: string) {
    if (selected && !options.some(({ value }) => selected === value)) {
      options.unshift({ value: selected, label: fallback });
    }
    return [{ value: '', label: all }, ...options];
  }

  function groupValue(group: UsageGroup) {
    return unit === 'tokens' ? group.tokens : group.estimatedCost;
  }

  function selectGroup(group: UsageGroup) {
    if (ranking === 'models') update('model', model === group.id ? '' : group.id);
    else update('client', client === group.id ? '' : group.id);
  }

  function viewRequests() {
    const heading = document.getElementById('requests-title');
    heading?.focus({ preventScroll: true });
    heading?.scrollIntoView({ block: 'start' });
  }
</script>

{#snippet allowanceSection(windows: Allowance[])}
  <section class="allowance-section" aria-labelledby="allowance-title">
    <header class="section-head">
      <h2 id="allowance-title">Codex allowance</h2>
      <Clock3Icon size={16} aria-hidden="true" />
    </header>
    {#each windows as allowance (allowance.id)}
      <div class="allowance-row">
        <div class="allowance-heading"><strong>{allowance.label}</strong><span>{Math.round(allowance.remainingPercent)}% left</span></div>
        <Meter value={allowance.remainingPercent} aria-label={allowance.label + ' remaining'} class="allowance-meter">
          <MeterTrack class="allowance-track"><MeterIndicator class="allowance-indicator" /></MeterTrack>
        </Meter>
        {#if allowance.resetsAt}<span class="muted">Resets {formatDateTime(allowance.resetsAt)}</span>{/if}
      </div>
    {/each}
  </section>
{/snippet}

{#snippet chartSelection()}
  <div class="chart-selection">
    <span aria-live="polite">{bucketLabel ? `Requests for ${bucketLabel}` : 'Choose a time window to filter requests.'}</span>
    {#if bucket}
      <Button class="icon-button" title="Clear chart time filter" aria-label="Clear chart time filter" onclick={() => update('bucket', '')} onkeydown={guardEnter}><XIcon size={14} aria-hidden="true" /></Button>
    {/if}
  </div>
{/snippet}

<header class="page-head">
  <div>
    <h1>Usage overview</h1>
    {#if updatedAt}<p class="update-line"><span class="live-dot"></span> Updated {formatTime(updatedAt)}</p>{/if}
  </div>
  {#if showFilters}
    <SegmentedControl class="segmented" aria-label="Date range" value={range} onValueChange={(value) => { if (value) update('range', value); }}>
      <SegmentedControlItem value="24h">Last 24h</SegmentedControlItem>
      <SegmentedControlItem value="7d">7 days</SegmentedControlItem>
      <SegmentedControlItem value="30d">30 days</SegmentedControlItem>
    </SegmentedControl>
  {/if}
</header>

{#if showFilters}
  <div class="filters">
    <Filter label="Source" value={source} items={sourceOptions} onChange={(value) => update('source', value)} />
    <Filter label="Client" value={client} items={clientOptions} onChange={(value) => update('client', value)} />
    <Filter label="Model" value={model} items={modelOptions} onChange={(value) => update('model', value)} />
    {#if scoped || range !== '24h'}
      <Button class="secondary-button" onclick={clearFilters} onkeydown={guardEnter}>Clear all</Button>
    {/if}
  </div>
{/if}

{#if error}
  <div class="feedback error-feedback" role="alert">
    <span>{error}</span>
    <Button class="secondary-button" onclick={refresh} disabled={loading}>Retry</Button>
  </div>
{/if}

{#if !data}
  {#if !error}
    <div class="usage-loading" role="status">
      <span class="muted">Loading usage…</span>
      <div class="loading-metrics" aria-hidden="true"><span></span><span></span></div>
      <div class="loading-charts" aria-hidden="true"><span></span><span></span></div>
      <div class="loading-table" aria-hidden="true"></div>
    </div>
  {/if}
{:else if data.summary.requestCount === 0}
  <section class="usage-empty" aria-labelledby="empty-usage-title">
    {#if firstRun}
      <h2 id="empty-usage-title">No clients connected</h2>
      <p>Create a client to get a proxy key and set up your first request.</p>
      <a class="primary-button" href="#clients" title="Connect a client (Enter)" aria-keyshortcuts="Enter" onkeydown={guardEnter}>Connect a client</a>
    {:else}
      <h2 id="empty-usage-title">No requests in this view</h2>
      {#if bucket}{@render chartSelection()}{/if}
      {#if scoped}
        <p>No requests match your filters. Clear them or select a different date range.</p>
      {:else}
        <p>Send a request from a configured client to see its usage here.</p>
      {/if}
      {#if range !== '30d'}
        <Button class="secondary-button" onclick={() => update('range', '30d')} onkeydown={guardEnter}>Show last 30 days</Button>
      {/if}
      {#if !scoped}<a class="secondary-button" href="#clients">View clients</a>{/if}
    {/if}
  </section>
  {#if data.allowance.length}{@render allowanceSection(data.allowance)}{/if}
{:else}
  <div class="metrics">
    <section class="metric" aria-label="Estimated usage">
      <div class="metric-label">Estimated usage</div>
      <strong>{data.summary.pricedRequestCount === 0 ? 'Unpriced' : money(data.summary.estimatedCost)}</strong>
      {#if data.summary.pricedRequestCount != null}
        <span class="metric-caption">{count(data.summary.pricedRequestCount)} of {count(data.summary.requestCount)} {data.summary.requestCount === 1 ? 'request' : 'requests'} priced</span>
      {/if}
      <span class="metric-caption cost-explanation">Reported charges, list values, and estimates.</span>
    </section>
    <section class="metric" aria-label="Total tokens">
      <div class="metric-label">Total tokens</div>
      <strong title={count(tokens(data.summary))}>{compactCount(tokens(data.summary))}</strong>
      <span class="metric-caption">{count(data.summary.requestCount)} {data.summary.requestCount === 1 ? 'request' : 'requests'}</span>
    </section>
  </div>

  <div class="chart-toggle">
    <SegmentedControl class="segmented" aria-label="Chart metric" value={chartMetric} onValueChange={(value) => { if (value) chartMetric = value; }}>
      <SegmentedControlItem value="cost">Cost</SegmentedControlItem>
      <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
    </SegmentedControl>
  </div>
  <div class="chart-row">
    <section class="chart-section" class:inactive-chart={chartMetric !== 'cost'} aria-labelledby="cost-chart-title">
      <header>
        <h2 id="cost-chart-title">Estimated usage over time</h2>
        {#if data.buckets.some(unpricedBucket)}
          <div class="legend"><span><i class="unpriced-mark"></i>Unpriced window</span></div>
        {/if}
      </header>
      <TimeChart buckets={data.buckets} kind="cost" {range} selection={bucket} onSelect={(value) => update('bucket', bucket === value ? '' : value)} />
    </section>
    <section class="chart-section" class:inactive-chart={chartMetric !== 'tokens'} aria-labelledby="token-chart-title">
      <header>
        <h2 id="token-chart-title">Tokens over time</h2>
        <div class="legend">
          <span><i class="input-color"></i>Uncached input</span>
          <span><i class="cache-color"></i>Cached input</span>
          <span><i class="output-color"></i>Output</span>
        </div>
      </header>
      <TimeChart buckets={data.buckets} kind="tokens" {range} selection={bucket} onSelect={(value) => update('bucket', bucket === value ? '' : value)} />
    </section>
  </div>
  <div class="chart-actions">
    <Filter label="Request time window" value={bucket} items={windowOptions} onChange={(value) => update('bucket', value)} />
    <Button class="secondary-button" title="View requests (Enter)" aria-keyshortcuts="Enter" onclick={viewRequests} onkeydown={guardEnter}>View requests</Button>
    {@render chartSelection()}
  </div>

  <div class="secondary-row" class:with-allowance={data.allowance.length > 0}>
    <section aria-labelledby="ranking-title">
      <header class="section-head rank-heading">
        <h2 id="ranking-title">Usage breakdown</h2>
        <div class="rank-controls">
          <SegmentedControl class="segmented" aria-label="Group usage by" value={ranking} onValueChange={(value) => { if (value) ranking = value; }}>
            <SegmentedControlItem value="models">Model</SegmentedControlItem>
            <SegmentedControlItem value="clients">Client</SegmentedControlItem>
          </SegmentedControl>
          <SegmentedControl class="segmented" aria-label="Breakdown unit" value={unit} onValueChange={(value) => { if (value) unit = value; }}>
            <SegmentedControlItem value="dollars">Est. USD</SegmentedControlItem>
            <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
          </SegmentedControl>
        </div>
      </header>
      {#if groups.length}
        <div class="rank-list">
          {#each groups as group (group.id)}
            {@const value = groupValue(group)}
            {@const coverage = unit === 'dollars' ? partialPricing(group) : ''}
            <button
              type="button"
              class="rank-item"
              aria-pressed={ranking === 'models' ? model === group.id : client === group.id}
              disabled={!group.id}
              onclick={() => selectGroup(group)}
              onkeydown={guardEnter}
            >
              <span class="rank-name" title={group.name}>{group.name}</span>
              <span class="rank-track" aria-hidden="true">
                {#if maxRank > 0}
                  <span class="usage-color" style:width={(100 * value / maxRank) + '%'}></span>
                {/if}
              </span>
              <span class="rank-amount">
                <strong>{unit === 'tokens' ? compactCount(value) : group.pricedRequestCount === 0 ? 'Unpriced' : money(value)}</strong>
                {#if coverage}<span class="muted">{coverage}</span>{/if}
              </span>
            </button>
          {/each}
        </div>
      {:else}
        <div class="empty-state compact-empty">No usage to rank in this view.</div>
      {/if}
    </section>

    {#if data.allowance.length}{@render allowanceSection(data.allowance)}{/if}
  </div>

  <RequestList requests={requestPage?.requests} {bucketLabel} {loading} {canPrevious} canNext={Boolean(requestPage?.nextCursor)} {onPrevious} {onNext} />
{/if}

<style>
  .update-line { display: flex; align-items: center; gap: 7px; font-size: 12px; }
  .metrics, .chart-row, .secondary-row { display: grid; border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .usage-empty { border-block-start: 1px solid var(--line); padding-block: var(--section-space); }
  .usage-empty h2 { margin-block-end: 6px; }
  .usage-empty p { max-inline-size: 55ch; color: var(--muted); }
  .usage-empty a { text-decoration: none; }
  .usage-loading { display: grid; gap: var(--gap); border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .loading-metrics, .loading-charts { display: grid; gap: var(--section-space); grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .loading-metrics span, .loading-charts span, .loading-table { border-radius: 6px; background: var(--surface); }
  .loading-metrics span { min-block-size: 55px; }
  .loading-charts span { min-block-size: 230px; }
  .loading-table { min-block-size: 190px; }
  .metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); padding-block-end: 6px; }
  .metric { display: flex; flex-direction: column; align-items: flex-start; padding-inline-end: var(--section-space); }
  .metric + .metric, .chart-section + .chart-section, .with-allowance > .allowance-section { border-inline-start: 1px solid var(--line); padding-inline-start: var(--section-space); }
  .metric-label { color: var(--muted); font-size: 12px; }
  .metric strong { display: block; margin: 4px 0 2px; font-size: clamp(22px, 3.2vw, 36px); font-weight: 620; letter-spacing: -.035em; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .metric-caption { color: var(--muted); font-size: 12px; }
  .cost-explanation { margin-block-start: 4px; }
  .chart-toggle { display: none; }
  .chart-row { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .chart-section { display: grid; grid-template-rows: 1fr auto; gap: 17px; min-inline-size: 0; }
  .chart-section:first-child { padding-inline-end: var(--section-space); }
  .legend { display: flex; gap: 13px; align-items: center; flex-wrap: wrap; font-size: 12px; color: var(--muted); margin-block-start: 8px; }
  .legend span { display: inline-flex; gap: 5px; align-items: center; white-space: nowrap; }
  .legend i { width: 8px; height: 8px; border-radius: 2px; }
  .legend .unpriced-mark { block-size: 0; inline-size: 14px; border-block-end: 2px dotted var(--muted); }
  .chart-actions, .chart-selection { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .chart-actions :global(.filter-trigger) { min-inline-size: 145px; }
  .chart-selection { color: var(--muted); font-size: 12px; min-block-size: 30px; }
  .chart-selection :global(.icon-button) { border-color: transparent; background: transparent; }
  .secondary-row.with-allowance { grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr); gap: var(--section-space); }
  .rank-heading { flex-wrap: wrap; }
  .rank-controls { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .rank-list { display: grid; grid-template-columns: minmax(92px, 1fr) minmax(50px, 3.2fr) minmax(48px, auto); gap: 4px 11px; }
  .rank-item { display: grid; grid-column: 1 / -1; grid-template-columns: subgrid; align-items: center; text-align: start; padding: 6px 0; background: none; border: 0; cursor: pointer; border-radius: 5px; }
  .rank-item:is(:hover, [aria-pressed=true]):not(:disabled) { background: var(--hover); }
  .rank-item:disabled { cursor: default; }
  .rank-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
  .rank-track { background: var(--line); border-radius: 3px; display: flex; block-size: 10px; overflow: hidden; }
  .rank-amount { display: grid; text-align: end; font-size: 12px; font-variant-numeric: tabular-nums; }
  .rank-amount strong { font-weight: 540; }
  .compact-empty { min-height: 73px; padding: 12px; }
  .allowance-section .section-head :global(svg) { color: var(--muted); }
  .allowance-row { margin-block-end: 14px; display: grid; gap: 5px; font-size: 12px; }
  .allowance-heading { display: flex; align-items: baseline; justify-content: space-between; font-size: 12px; gap: 8px; }
  .allowance-heading strong { font-weight: 540; }
  .allowance-heading span { color: var(--codex-light); white-space: nowrap; }
  :global(.allowance-track) { background: var(--line); border-radius: 4px; overflow: hidden; block-size: 6px; }
  :global(.allowance-indicator) { display: block; block-size: 100%; background: var(--codex); width: var(--meter-value, 0%); }
  @media (max-width: 900px) {
    .chart-row, .secondary-row.with-allowance, .loading-charts { grid-template-columns: minmax(0, 1fr); gap: var(--section-space); }
    .chart-section:first-child { padding-inline-end: 0; }
    .chart-section + .chart-section, .with-allowance > .allowance-section { border-inline-start: 0; border-block-start: 1px solid var(--line); padding: var(--section-space) 0 0; }
  }
  @media (max-width: 670px) {
    .chart-toggle { display: flex; border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
    .chart-row, .chart-section + .chart-section { border: 0; padding: 0; }
    .chart-section.inactive-chart, .loading-charts span + span { display: none; }
    .chart-actions { display: grid; grid-template-columns: minmax(0, 1fr) auto; }
    .chart-actions :global(.filter-trigger) { min-inline-size: 0; max-inline-size: none; }
    .chart-actions :global(.filter-trigger), .chart-actions :global(.secondary-button) { min-block-size: 44px; }
    .chart-selection { grid-column: 1 / -1; }
    .chart-selection :global(.icon-button) { inline-size: 36px; block-size: 36px; }
    .loading-charts span { min-block-size: 240px; }
  }
  @media (max-width: 600px) {
    .metrics { grid-template-columns: 1fr; padding-block: 0; }
    .metric { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 3px 10px; padding: 10px 0; align-items: baseline; }
    .metric + .metric { border-inline-start: 0; border-block-start: 1px solid var(--line); padding-inline-start: 0; }
    .metric strong { margin: 0; grid-column: 2; grid-row: 1 / span 2; align-self: center; }
    .metric-caption { grid-column: 1; }
    .cost-explanation { grid-column: 1 / -1; }
    .loading-metrics { grid-template-columns: minmax(0, 1fr); gap: 8px; }
    .loading-metrics span { min-block-size: 47px; }
  }
</style>
