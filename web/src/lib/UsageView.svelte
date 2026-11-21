<script lang="ts">
  import Clock3Icon from '@lucide/svelte/icons/clock-3';
  import RefreshCwIcon from '@lucide/svelte/icons/refresh-cw';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Meter, MeterIndicator, MeterTrack } from '@ui-sdk/svelte/meter';
  import { SegmentedControl, SegmentedControlItem } from '@ui-sdk/svelte/segmented-control';
  import type { Allowance, DashboardProblem, RequestOutcome, Usage, UsageGroup } from './api';
  import { compactCount, count, formatDateTime, formatTime, formatTimeWindow, formatUTCDay, guardEnter, money, partialPricing, tokens, unpricedBucket } from './format';
  import Filter from './Filter.svelte';
  import RequestList from './RequestList.svelte';
  import RecoveryNotice from './RecoveryNotice.svelte';
  import TimeChart, { tokenSeries } from './TimeChart.svelte';

  type ChartMetric = 'cost' | 'tokens' | 'compare';

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
    range, source, client, model, bucket, outcome, canPrevious, pageNumber, chartMetric = $bindable('cost'), update,
    clearFilters, canUndoReset, undoFilters, focusRequests, onRequestFocus, refresh, retryRequests, onPrevious, onNext
  }: Props = $props();
  let ranking = $state('models');
  let unit = $state('dollars');
  let resetStatus = $state('');
  const chartTitles = { cost: 'Usage value over time', tokens: 'Tokens over time', compare: 'Compare usage over time' };
  let chartTitle = $derived(chartTitles[chartMetric]);
  let scoped = $derived(Boolean(source || client || model || bucket || outcome));
  let loading = $derived(loadingScope !== null);
  let firstRun = $derived(data?.summary.requestCount === 0 && !data.filters.clients.length);
  let showFilters = $derived(!firstRun || scoped || range !== '24h' || canUndoReset);
  let resetCommand = $derived(canUndoReset ? 'Undo reset' : 'Reset filters');
  let bucketLabel = $derived(bucket ? range === '24h' ? formatTimeWindow(bucket, 1) : formatUTCDay(bucket) + ' UTC' : '');
  let rankedGroups = $derived(
    (data?.groups[ranking === 'models' ? 'models' : 'clients'] ?? [])
      .slice().sort((a, b) => groupValue(b) - groupValue(a) || a.name.localeCompare(b.name))
  );
  let groups = $derived(rankedGroups.slice(0, 6));
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
    const filter = ranking === 'models' ? 'model' : 'client';
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
    if (value === 'cost' || value === 'tokens' || value === 'compare') chartMetric = value;
  }

  function retryOverview() {
    const target = document.getElementById(data ? 'refresh-usage' : 'usage-title');
    target?.focus({ preventScroll: true });
    refresh();
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
  {#if data.allowance.length}{@render allowanceSection(data.allowance)}{/if}
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
      <SegmentedControl class="segmented" aria-label="Chart metric" value={chartMetric} onValueChange={changeChartMetric}>
        <SegmentedControlItem value="cost">Usage value</SegmentedControlItem>
        <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
        <SegmentedControlItem value="compare">Compare</SegmentedControlItem>
      </SegmentedControl>
    </header>
    <div class="chart-row" class:compare={chartMetric === 'compare'}>
      {#if chartMetric !== 'tokens'}
        <section class="chart-section" aria-labelledby={chartMetric === 'compare' ? 'cost-chart-title' : 'chart-heading'}>
          <header>
            {#if chartMetric === 'compare'}<h3 id="cost-chart-title">{chartTitles.cost}</h3>{/if}
            {#if data.buckets.some(unpricedBucket)}
              <div class="legend"><span><i class="unpriced-mark"></i>Unpriced window</span></div>
            {/if}
          </header>
          <TimeChart buckets={data.buckets} kind="cost" {range} selection={bucket} onSelect={(value) => update('bucket', bucket === value ? '' : value)} />
        </section>
      {/if}
      {#if chartMetric !== 'cost'}
        <section class="chart-section" aria-labelledby={chartMetric === 'compare' ? 'token-chart-title' : 'chart-heading'}>
          <header>
            {#if chartMetric === 'compare'}<h3 id="token-chart-title">{chartTitles.tokens}</h3>{/if}
            <div class="legend">
              {#each tokenSeries as series (series.key)}
                <span><i class={series.key + '-color'} data-series={series.key} aria-hidden="true"></i>{series.label}</span>
              {/each}
            </div>
          </header>
          <TimeChart buckets={data.buckets} kind="tokens" {range} selection={bucket} onSelect={(value) => update('bucket', bucket === value ? '' : value)} />
        </section>
      {/if}
    </div>
    <div class="chart-actions">
      {@render overviewFilter('request-window-filter', 'Request time window', bucket, windowOptions, (value) => update('bucket', value))}
      <Button class="secondary-button" title="View requests (Enter)" aria-keyshortcuts="Enter" onclick={viewRequests} onkeydown={guardEnter}>View requests</Button>
      {@render chartSelection()}
    </div>
  </div>

  <RequestList
    requests={requestPage?.requests} {bucketLabel} {loading} {canPrevious} canNext={Boolean(requestPage?.nextCursor)}
    {outcome} onOutcomeChange={(value) => update('outcome', value)} {pageNumber} error={requestError} retry={retryRequests}
    {onPrevious} {onNext}
  />

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
            <SegmentedControlItem value="dollars">Usage value</SegmentedControlItem>
            <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
          </SegmentedControl>
        </div>
      </header>
      {#if groups.length}
        <p class="rank-hint muted">
          {#if rankedGroups.length > groups.length}
            Showing the top {groups.length} of {rankedGroups.length} {ranking}.
          {/if}
          Select a row to filter usage.
        </p>
        <div class="rank-list">
          {#each groups as group (group.id)}
            {@const value = groupValue(group)}
            {@const coverage = unit === 'dollars' ? partialPricing(group) : ''}
            <button
              type="button"
              class="rank-item"
              aria-pressed={ranking === 'models' ? model === group.id : client === group.id}
              aria-keyshortcuts={group.id ? 'Enter Space' : undefined}
              disabled={!group.id}
              onclick={() => selectGroup(group)}
              onkeydown={guardEnter}
            >
              <span class="rank-name" title={group.name}>
                {#if group.id}<span class="sr-only">Filter by {ranking === 'models' ? 'model' : 'client'} </span>{/if}
                {group.name}
              </span>
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
{/if}

<style>
  .update-line { display: flex; align-items: center; gap: 7px; font-size: 12px; margin-block-start: 4px; }
  .update-line p { display: flex; align-items: center; gap: 6px; margin: 0; }
  .update-line :global(.refresh-button) { background: transparent; border-color: transparent; }
  .metrics, .timeline, .secondary-row { display: grid; border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .filters { align-items: flex-end; }
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
  .metric + .metric, .compare .chart-section + .chart-section, .with-allowance > .allowance-section { border-inline-start: 1px solid var(--line); padding-inline-start: var(--section-space); }
  .metric-label { color: var(--muted); font-size: 12px; }
  .metric strong { display: block; margin: 4px 0 2px; font-size: clamp(22px, 3.2vw, 36px); font-weight: 620; letter-spacing: -.035em; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .metric-caption { color: var(--muted); font-size: 12px; }
  .cost-explanation { margin-block-start: 4px; }
  .chart-heading { flex-wrap: wrap; }
  .chart-row { display: grid; grid-template-columns: minmax(0, 1fr); }
  .chart-row.compare { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--section-space); }
  .chart-section { display: grid; gap: 10px; min-inline-size: 0; }
  .chart-section header:empty { display: none; }
  .compare .chart-section { grid-template-rows: 1fr auto; }
  .chart-section h3 { font-size: 14px; margin-block-end: 0; }
  .legend { display: flex; gap: 13px; align-items: center; flex-wrap: wrap; font-size: 12px; color: var(--muted); }
  .chart-section h3 + .legend { margin-block-start: 6px; }
  .legend span { display: inline-flex; gap: 5px; align-items: center; white-space: nowrap; }
  .legend i { width: 8px; height: 8px; border-radius: 2px; }
  .legend .unpriced-mark { block-size: 0; inline-size: 14px; border-block-end: 2px dotted var(--muted); }
  .chart-actions, .chart-selection { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .chart-actions { align-items: flex-end; margin-block-start: 12px; }
  .chart-actions :global(.filter-trigger) { min-inline-size: 145px; }
  .chart-selection { color: var(--muted); font-size: 12px; min-block-size: 30px; }
  .chart-selection:not(.has-selection) { position: absolute; inline-size: 1px; block-size: 1px; min-block-size: 0; overflow: hidden; clip-path: inset(50%); }
  .chart-selection :global(.icon-button) { border-color: transparent; background: transparent; }
  .secondary-row.with-allowance { grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr); gap: var(--section-space); }
  .rank-heading { flex-wrap: wrap; }
  .rank-hint { margin: 0 0 6px; font-size: 12px; }
  .rank-controls { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .rank-list { display: grid; grid-template-columns: minmax(92px, 1fr) minmax(50px, 3.2fr) minmax(48px, auto); gap: 4px 11px; }
  .rank-item { display: grid; grid-column: 1 / -1; grid-template-columns: subgrid; align-items: center; text-align: start; padding: 6px 0; background: none; border: 0; cursor: pointer; border-radius: 5px; }
  .rank-item:is(:hover, [aria-pressed=true]):not(:disabled) { background: var(--hover); }
  .rank-item[aria-pressed=true]:not(:disabled) { box-shadow: inset 0 0 0 1px var(--selected-border); }
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
  @media (pointer: coarse) {
    .rank-item { min-block-size: var(--touch-target); }
  }
  @media (forced-colors: active) {
    .timeline {
      --input: CanvasText;
      --cache: repeating-linear-gradient(135deg, CanvasText 0 1px, Canvas 1px 3px);
      --cache-write: repeating-linear-gradient(0deg, CanvasText 0 1px, Canvas 1px 3px);
      --output: repeating-linear-gradient(90deg, CanvasText 0 1px, Canvas 1px 3px);
    }
    .legend i[data-series] { forced-color-adjust: none; }
    .rank-track, :global(.allowance-track) { border: 1px solid CanvasText; }
    .rank-track .usage-color, :global(.allowance-indicator) { forced-color-adjust: none; background: CanvasText; }
    .rank-item[aria-pressed=true]:not(:disabled) { position: relative; }
    .rank-item[aria-pressed=true]:not(:disabled)::after { content: ''; position: absolute; inset: 0; border: 1px solid Highlight; border-radius: inherit; pointer-events: none; }
  }
  @media (max-width: 900px) {
    .chart-row.compare, .secondary-row.with-allowance { grid-template-columns: minmax(0, 1fr); gap: var(--section-space); }
    .compare .chart-section + .chart-section, .with-allowance > .allowance-section { border-inline-start: 0; border-block-start: 1px solid var(--line); padding: var(--section-space) 0 0; }
  }
  @media (max-width: 670px) {
    .chart-actions { display: grid; grid-template-columns: minmax(0, 1fr) auto; }
    .chart-actions :global(.filter-trigger) { min-inline-size: 0; max-inline-size: none; }
    .chart-actions :global(.filter-trigger), .chart-actions :global(.secondary-button), .filters :global(button) { min-block-size: 44px; }
    .chart-selection { grid-column: 1 / -1; }
    .chart-selection :global(.icon-button) { inline-size: 36px; block-size: 36px; }
    .loading-chart { min-block-size: 240px; }
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
