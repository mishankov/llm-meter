<script lang="ts">
  import RefreshCwIcon from '@lucide/svelte/icons/refresh-cw';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { SegmentedControl, SegmentedControlItem } from '@ui-sdk/svelte/segmented-control';
  import type { DashboardProblem, RequestOutcome, Usage, UsageGroup } from './api';
  import { compactCount, count, formatDateTime, formatTime, formatTimeWindow, formatUTCDay, guardEnter, money, partialPricing, tokens, unpricedBucket } from './format';
  import Filter from './Filter.svelte';
  import RequestList from './RequestList.svelte';
  import RecoveryNotice from './RecoveryNotice.svelte';
  import TimeChart, { groupedSeries, tokenSeries, type ChartSeries } from './TimeChart.svelte';

  type ChartMetric = 'cost' | 'tokens';
  type Dimension = 'models' | 'clients';

  interface Props {
    data: Usage | null;
    filterOptions?: Usage['filters'];
    requestPage: Pick<Usage, 'requests' | 'nextCursor'> | null;
    loadingScope: 'overview' | 'requests' | null;
    online: boolean;
    overviewError: DashboardProblem | null;
    requestError: DashboardProblem | null;
    updatedAt: string;
    range: string;
    source: string;
    client: string;
    model: string;
    bucket: string;
    outcome: RequestOutcome;
    canPrevious: boolean;
    pageNumber: number;
    chartMetric?: ChartMetric;
    update: (name: 'range' | 'source' | 'client' | 'model' | 'bucket' | 'outcome', value: string) => void;
    clearFilters: () => void;
    canUndoReset: boolean;
    undoFilters: () => void;
    focusRequests: boolean;
    onRequestFocus: () => void;
    refresh: () => void;
    retryRequests: () => void;
    onPrevious: () => void;
    onNext: () => void;
  }

  let {
    data, filterOptions, requestPage, loadingScope, online, overviewError, requestError, updatedAt,
    range, source, client, model, bucket, outcome, canPrevious, pageNumber, chartMetric = $bindable('tokens'), update,
    clearFilters, canUndoReset, undoFilters, focusRequests, onRequestFocus, refresh, retryRequests, onPrevious, onNext
  }: Props = $props();
  let dimension = $state<Dimension>('models');
  let resetStatus = $state('');
  let chartTitle = $derived(chartMetric === 'tokens' ? 'Tokens over time' : 'Usage value over time');
  let scoped = $derived(Boolean(source || client || model || bucket || outcome));
  let loading = $derived(loadingScope !== null);
  let firstRun = $derived(data?.summary.requestCount === 0 && !data.filters.clients.length);
  let showFilters = $derived(!firstRun || scoped || range !== '24h' || canUndoReset);
  let resetCommand = $derived(canUndoReset ? 'Undo reset' : 'Reset filters');
  let bucketLabel = $derived(bucket ? range === '24h' ? formatTimeWindow(bucket, 1) : formatUTCDay(bucket) + ' UTC' : '');
  const percentage = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 });
  let chartSeries = $derived(getSeries(chartMetric === 'tokens' ? 'tokens' : 'estimatedCost'));

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

  function getSeries(metric: 'estimatedCost' | 'tokens') {
    if (!data?.groupedBuckets) return undefined;
    return groupedSeries(data.groupedBuckets[dimension], data.groups[dimension], dimension === 'models' ? 'Unknown model' : 'Unknown client', metric);
  }

  function share(value: number, total: number) {
    return total > 0 ? percentage.format(value / total) : '—';
  }

  function selectGroup(group: UsageGroup, filter: 'model' | 'client') {
    const control = document.getElementById(filter + '-filter')?.querySelector<HTMLElement>('[role="combobox"]');
    control?.focus({ preventScroll: true });
    control?.scrollIntoView({ block: 'center' });
    update(filter, (filter === 'model' ? model : client) === group.id ? '' : group.id);
  }

  function viewRequests() {
    const heading = document.getElementById('requests-title') ?? document.getElementById('empty-usage-title') ?? document.getElementById('usage-title');
    heading?.focus({ preventScroll: true });
    heading?.scrollIntoView({ block: 'start' });
  }

  $effect(() => {
    if (!focusRequests || loading || !(requestPage || requestError || overviewError || data?.summary.requestCount === 0)) return;
    viewRequests();
    onRequestFocus();
  });

  function resetOrUndoFilters() {
    if (canUndoReset) {
      undoFilters();
      resetStatus = 'Previous filters restored.';
    } else {
      clearFilters();
      resetStatus = 'Filters reset.';
    }
  }

  function clearRequestWindow() {
    const control = document.getElementById('request-window-filter')?.querySelector<HTMLElement>('[role="combobox"]')
      ?? document.getElementById('empty-usage-title');
    control?.focus({ preventScroll: true });
    update('bucket', '');
  }

  function changeChartMetric(value: string | null) {
    if (value === 'cost' || value === 'tokens') chartMetric = value;
  }

  function changeDimension(value: string | null) {
    if (value === 'models' || value === 'clients') dimension = value;
  }

  function retryOverview() {
    const target = document.getElementById(data ? 'refresh-usage' : 'usage-title');
    target?.focus({ preventScroll: true });
    refresh();
  }
</script>

{#snippet breakdown(groups: UsageGroup[], filter: 'model' | 'client', totalTokens: number, totalCost: number)}
  {@const heading = filter === 'model' ? 'By model' : 'By client'}
  {@const ranked = groups.slice().sort((a, b) => b.estimatedCost - a.estimatedCost || b.tokens - a.tokens || a.name.localeCompare(b.name))}
  <section class="breakdown-panel" aria-labelledby={filter + '-breakdown-title'}>
    <h3 id={filter + '-breakdown-title'}>{heading}</h3>
    {#if ranked.length}
      <table>
        <thead><tr><th scope="col">{filter === 'model' ? 'Model' : 'Client'}</th><th scope="col">Usage value</th><th scope="col">Tokens</th></tr></thead>
        <tbody>
          {#each ranked.slice(0, 6) as group (group.id)}
            {@const priced = group.pricedRequestCount !== 0}
            {@const coverage = partialPricing(group)}
            <tr>
              <th scope="row">
                {#if group.id}
                  <Button class="breakdown-filter" title={group.name + ' (Enter or Space)'}
                    aria-label={'Filter by ' + filter + ' ' + group.name} aria-keyshortcuts="Enter Space"
                    aria-pressed={(filter === 'model' ? model : client) === group.id}
                    onclick={() => selectGroup(group, filter)} onkeydown={guardEnter}>{group.name}</Button>
                {:else}
                  {group.name}
                {/if}
              </th>
              <td><strong>{priced ? money(group.estimatedCost) : 'Unpriced'}</strong>
                {#if priced}<span>{share(group.estimatedCost, totalCost)}</span>{/if}
                {#if coverage}<span>{coverage}</span>{/if}
              </td>
              <td><strong>{compactCount(group.tokens)}</strong><span>{share(group.tokens, totalTokens)}</span></td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if ranked.length > 6}<p class="muted breakdown-note">Showing 6 of {count(ranked.length)} {filter}s.</p>{/if}
    {:else}
      <p class="muted breakdown-note">No usage by {filter} in this view.</p>
    {/if}
  </section>
{/snippet}

{#snippet groupLegend(series: ChartSeries[] | undefined, markUnpriced = false)}
  {#if series?.length || markUnpriced}
    <div class="legend">
      {#each series ?? [] as group (group.key)}
        <span><i class="group-swatch" aria-hidden="true" style:background={'var(' + group.color + ')'}></i>{group.label}</span>
      {/each}
      {#if markUnpriced}<span><i class="unpriced-mark" aria-hidden="true"></i>Unpriced window</span>{/if}
    </div>
  {/if}
{/snippet}

{#snippet chartSelection()}
  <div class="chart-selection" class:has-selection={Boolean(bucket)}>
    <span aria-live="polite">{bucketLabel ? `Requests for ${bucketLabel}` : ''}</span>
    {#if bucket}
      <Button class="icon-button" title="Clear chart time filter (Enter)" aria-label="Clear chart time filter" aria-keyshortcuts="Enter" onclick={clearRequestWindow} onkeydown={guardEnter}>
        <XIcon size={14} aria-hidden="true" />
      </Button>
    {/if}
  </div>
{/snippet}

{#snippet overviewFilter(id: string, label: string, value: string, options: { value: string; label: string }[], onChange: (value: string) => void)}
  <div class="filter-field" {id}>
    <span aria-hidden="true">{label}</span>
    <Filter {label} {value} items={options} {onChange} />
  </div>
{/snippet}

<header class="page-head">
  <div>
    <h1 id="usage-title" tabindex="-1">Usage overview</h1>
    {#if updatedAt}
      <div class="update-line">
        <p>
          {#if !online}Offline · Last updated {formatTime(updatedAt)}
          {:else if loadingScope === 'overview'}Refreshing · Last updated {formatTime(updatedAt)}
          {:else if overviewError}Refresh failed · Last updated {formatTime(updatedAt)}
          {:else}<span class="live-dot" aria-hidden="true"></span> Updated {formatTime(updatedAt)}
          {/if}
        </p>
        <Button id="refresh-usage" class="icon-button refresh-button" aria-label="Refresh usage" title="Refresh usage (Enter)" aria-keyshortcuts="Enter" disabled={loading || !online} focusableWhenDisabled onclick={refresh} onkeydown={guardEnter}>
          <RefreshCwIcon size={15} aria-hidden="true" />
        </Button>
      </div>
    {/if}
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
    {@render overviewFilter('source-filter', 'Source', source, sourceOptions, (value) => update('source', value))}
    {@render overviewFilter('client-filter', 'Client', client, clientOptions, (value) => update('client', value))}
    {@render overviewFilter('model-filter', 'Model', model, modelOptions, (value) => update('model', value))}
    {#if scoped || range !== '24h' || canUndoReset}
      <Button class="secondary-button" title={resetCommand + ' (Enter)'} aria-keyshortcuts="Enter" onclick={resetOrUndoFilters} onkeydown={guardEnter}>{resetCommand}</Button>
    {/if}
    {#if data?.summary.requestCount}
      <Button class="secondary-button requests-jump" title="View requests (Enter)" aria-keyshortcuts="Enter" onclick={viewRequests} onkeydown={guardEnter}>View requests</Button>
    {/if}
  </div>
{/if}
<span class="sr-only" aria-live="polite" aria-atomic="true">{resetStatus}</span>

{#if overviewError}
  <RecoveryNotice problem={overviewError} retry={retryOverview} retryLabel="Retry usage" busy={loading || !online} cached={Boolean(data)} cachedMessage="Showing the last loaded data for these filters." />
{/if}

{#if !data}
  {#if !overviewError}
    <div class="usage-loading" role="status">
      <span class="muted">Loading usage…</span>
      <div class="loading-metrics" aria-hidden="true"><span></span><span></span></div>
      <div class="loading-chart" aria-hidden="true"></div>
      <div class="loading-table" aria-hidden="true"></div>
    </div>
  {/if}
{:else if data.summary.requestCount === 0}
  <section class="usage-empty" aria-labelledby="empty-usage-title">
    {#if firstRun}
      <h2 id="empty-usage-title" tabindex="-1">No clients connected</h2>
      <p>Create a client to get a proxy key and set up your first request.</p>
      <a class="primary-button" href="#clients" title="Connect a client (Enter)" aria-keyshortcuts="Enter" onkeydown={guardEnter}>Connect a client</a>
    {:else}
      <h2 id="empty-usage-title" tabindex="-1">No requests in this view</h2>
      {#if bucket}{@render chartSelection()}{/if}
      {#if scoped}
        <p>No requests match your filters. Reset the filters or choose another date range.</p>
      {:else}
        <p>Send a request from a configured client to see its usage here.</p>
      {/if}
      {#if range !== '30d'}
        <Button class="secondary-button" onclick={() => update('range', '30d')} onkeydown={guardEnter}>Show last 30 days</Button>
      {/if}
      {#if !scoped}<a class="secondary-button" href="#clients">View clients</a>{/if}
    {/if}
  </section>
{:else}
  <div class="metrics">
    <section class="metric" aria-label="Usage value">
      <div class="metric-label">Usage value</div>
      <strong>{data.summary.pricedRequestCount === 0 ? 'Unpriced' : money(data.summary.estimatedCost)}</strong>
      {#if data.summary.pricedRequestCount != null}
        <span class="metric-caption">{count(data.summary.pricedRequestCount)} of {count(data.summary.requestCount)} {data.summary.requestCount === 1 ? 'request' : 'requests'} priced</span>
      {/if}
      <span class="metric-caption cost-explanation">Reported charges, provider list values and estimates, in USD.</span>
    </section>
    <section class="metric" aria-label="Total tokens">
      <div class="metric-label">Total tokens</div>
      <strong title={count(tokens(data.summary))}>{compactCount(tokens(data.summary))}</strong>
      <span class="metric-caption">{count(data.summary.requestCount)} {data.summary.requestCount === 1 ? 'request' : 'requests'}</span>
    </section>
  </div>

  <div class="timeline">
    <header class="section-head chart-heading">
      <h2 id="chart-heading">{chartTitle}</h2>
      <div class="chart-controls">
        {#if data.groupedBuckets}
          <SegmentedControl class="segmented" aria-label="Group timeline by" value={dimension} onValueChange={changeDimension}>
            <SegmentedControlItem value="models">Models</SegmentedControlItem>
            <SegmentedControlItem value="clients">Clients</SegmentedControlItem>
          </SegmentedControl>
        {/if}
        <SegmentedControl class="segmented" aria-label="Chart metric" value={chartMetric} onValueChange={changeChartMetric}>
          <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
          <SegmentedControlItem value="cost">Usage value</SegmentedControlItem>
        </SegmentedControl>
      </div>
    </header>
    <section class="chart-section" aria-labelledby="chart-heading">
      <header>
        {#if chartMetric === 'tokens' && !chartSeries}
          <div class="legend">
            {#each tokenSeries as series (series.key)}
              <span><i class={series.key + '-color'} data-series={series.key} aria-hidden="true"></i>{series.label}</span>
            {/each}
          </div>
        {:else}
          {@render groupLegend(chartSeries, chartMetric === 'cost' && data.buckets.some(unpricedBucket))}
        {/if}
      </header>
      {#key chartMetric}
        <TimeChart buckets={data.buckets} kind={chartMetric} series={chartSeries} {range} selection={bucket} onSelect={(value) => update('bucket', bucket === value ? '' : value)} />
      {/key}
    </section>
    <div class="chart-actions">
      {@render overviewFilter('request-window-filter', 'Request time window', bucket, windowOptions, (value) => update('bucket', value))}
      {@render chartSelection()}
    </div>
  </div>

  <section class="breakdown" aria-labelledby="breakdown-title">
    <header class="section-head"><h2 id="breakdown-title">Period totals and shares</h2></header>
    <div class="breakdown-grid">
      {@render breakdown(data.groups.clients, 'client', tokens(data.summary), data.summary.estimatedCost)}
      {@render breakdown(data.groups.models, 'model', tokens(data.summary), data.summary.estimatedCost)}
    </div>
  </section>

  <RequestList
    requests={requestPage?.requests} {bucketLabel} {loading} {canPrevious} canNext={Boolean(requestPage?.nextCursor)}
    {outcome} onOutcomeChange={(value) => update('outcome', value)} {pageNumber} error={requestError} retry={retryRequests}
    {onPrevious} {onNext}
  />

{/if}

<style>
  .update-line { display: flex; align-items: center; gap: 7px; font-size: 12px; margin-block-start: 4px; }
  .update-line p { display: flex; align-items: center; gap: 6px; margin: 0; }
  .update-line :global(.refresh-button) { background: transparent; border-color: transparent; }
  .metrics, .timeline, .breakdown { display: grid; border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .filters { align-items: flex-end; }
  .filters :global(.requests-jump) { margin-inline-start: auto; }
  .filter-field { display: grid; gap: 4px; min-inline-size: 0; }
  .filter-field > span { color: var(--muted); font-size: 12px; }
  .usage-empty { border-block-start: 1px solid var(--line); padding-block: var(--section-space); }
  .usage-empty h2 { margin-block-end: 6px; scroll-margin-block-start: 8px; }
  .usage-empty p { max-inline-size: 55ch; color: var(--muted); }
  .usage-empty a { text-decoration: none; }
  .usage-loading { display: grid; gap: var(--gap); border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .loading-metrics { display: grid; gap: var(--section-space); grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .loading-metrics span, .loading-chart, .loading-table { border-radius: 6px; background: var(--surface); }
  .loading-metrics span { min-block-size: 55px; }
  .loading-chart { min-block-size: 230px; }
  .loading-table { min-block-size: 190px; }
  .metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); padding-block-end: 6px; }
  .metric { display: flex; flex-direction: column; align-items: flex-start; padding-inline-end: var(--section-space); }
  .metric + .metric { border-inline-start: 1px solid var(--line); padding-inline-start: var(--section-space); }
  .metric-label { color: var(--muted); font-size: 12px; }
  .metric strong { display: block; margin: 4px 0 2px; font-size: clamp(22px, 3.2vw, 36px); font-weight: 620; letter-spacing: -.035em; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .metric-caption { color: var(--muted); font-size: 12px; }
  .cost-explanation { margin-block-start: 4px; }
  .timeline {
    --usage-chart-1: var(--api-light);
    --usage-chart-2: var(--codex-light);
    --usage-chart-3: var(--output);
    --usage-chart-4: var(--cache);
    --usage-chart-5: var(--cache-write);
    --usage-chart-6: var(--input);
  }
  .chart-heading { flex-wrap: wrap; }
  .chart-controls { display: flex; gap: 6px; flex-wrap: wrap; }
  .chart-section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; min-inline-size: 0; }
  .chart-section header:empty { display: none; }
  .legend { display: flex; gap: 13px; align-items: center; flex-wrap: wrap; font-size: 12px; color: var(--muted); }
  .legend span { display: inline-flex; gap: 5px; align-items: center; overflow-wrap: anywhere; }
  .legend i { flex: none; width: 8px; height: 8px; border-radius: 2px; }
  .legend .unpriced-mark { block-size: 0; inline-size: 14px; border-block-end: 2px dotted var(--muted); }
  .chart-actions, .chart-selection { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .chart-actions { align-items: flex-end; margin-block-start: 12px; }
  .chart-actions :global(.filter-trigger) { min-inline-size: 145px; }
  .chart-selection { color: var(--muted); font-size: 12px; min-block-size: 30px; }
  .chart-selection:not(.has-selection) { position: absolute; inline-size: 1px; block-size: 1px; min-block-size: 0; overflow: hidden; clip-path: inset(50%); }
  .chart-selection :global(.icon-button) { border-color: transparent; background: transparent; }
  .breakdown-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--section-space); }
  .breakdown-panel { min-inline-size: 0; }
  .breakdown-panel h3 { font-size: 13px; margin: 0 0 5px; }
  .breakdown-panel table { inline-size: 100%; border-collapse: collapse; table-layout: fixed; }
  .breakdown-panel :is(th, td) { padding: 6px 0 6px 6px; border-block-end: 1px solid var(--line); text-align: end; }
  .breakdown-panel th:first-child { inline-size: 44%; padding-inline-start: 0; text-align: start; }
  .breakdown-panel thead th, .breakdown-panel td span { font-size: 11px; color: var(--muted); }
  .breakdown-panel td { font-size: 12px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
  .breakdown-panel :is(td strong, tbody th) { font-weight: 550; }
  .breakdown-panel td span { display: block; }
  .breakdown-panel :global(.breakdown-filter) { text-align: start; font: inherit; color: inherit; background: none; border: 0; padding: 4px 3px; border-radius: 4px; cursor: pointer; overflow-wrap: anywhere; max-inline-size: 100%; }
  .breakdown-panel :global(.breakdown-filter:hover) { background: var(--hover); }
  .breakdown-panel :global(.breakdown-filter[aria-pressed=true]) { box-shadow: inset 0 0 0 1px var(--selected-border); }
  .breakdown-note { margin: 7px 0 0; font-size: 12px; }
  @media (pointer: coarse) {
    .breakdown-panel :global(.breakdown-filter) { min-block-size: var(--touch-target); }
  }
  @media (forced-colors: active) {
    .timeline {
      --usage-chart-1: CanvasText;
      --usage-chart-2: repeating-linear-gradient(135deg, CanvasText 0 2px, Canvas 2px 4px);
      --usage-chart-3: repeating-linear-gradient(90deg, CanvasText 0 2px, Canvas 2px 4px);
      --usage-chart-4: repeating-linear-gradient(45deg, CanvasText 0 2px, Canvas 2px 4px);
      --usage-chart-5: repeating-linear-gradient(0deg, CanvasText 0 2px, Canvas 2px 4px);
      --usage-chart-6: radial-gradient(CanvasText 1px, Canvas 1.5px) 0 0 / 4px 4px;
      --input: CanvasText;
      --cache: repeating-linear-gradient(135deg, CanvasText 0 1px, Canvas 1px 3px);
      --cache-write: repeating-linear-gradient(0deg, CanvasText 0 1px, Canvas 1px 3px);
      --output: repeating-linear-gradient(90deg, CanvasText 0 1px, Canvas 1px 3px);
    }
    .legend :is(i[data-series], .group-swatch) { forced-color-adjust: none; }
    .legend .group-swatch { border: 1px solid CanvasText; }
    .breakdown-panel :global(.breakdown-filter[aria-pressed=true]) { outline: 1px solid Highlight; }
  }
  @media (max-width: 670px) {
    .chart-actions :global(.filter-trigger) { min-inline-size: 0; max-inline-size: none; }
    .chart-actions :global(.filter-trigger), .chart-actions :global(.secondary-button), .filters :global(button) { min-block-size: 44px; }
    .chart-selection :global(.icon-button) { inline-size: 36px; block-size: 36px; }
    .loading-chart { min-block-size: 240px; }
  }
  @media (max-width: 600px) {
    .breakdown-grid { grid-template-columns: minmax(0, 1fr); }
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
