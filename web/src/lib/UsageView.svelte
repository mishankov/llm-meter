<script lang="ts">
  import RefreshCwIcon from '@lucide/svelte/icons/refresh-cw';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { SegmentedControl, SegmentedControlItem } from '@ui-sdk/svelte/segmented-control';
  import { Toggle } from '@ui-sdk/svelte/toggle';
  import { UNREPORTED_REASONING, type DashboardProblem, type ReasoningMixEntry, type Usage, type UsageGroup } from './api';
  import { compactCount, count, formatDateTime, formatTime, formatTimeWindow, formatUTCDay, guardEnter, money, partialPricing, tokens, uncachedInput, unpricedBucket } from './format';
  import Filter from './Filter.svelte';
  import RequestList from './RequestList.svelte';
  import RecoveryNotice from './RecoveryNotice.svelte';
  import TimeChart, { tokenSeries } from './TimeChart.svelte';
  import type { UsageFilterChange, UsageFilters } from './usage-filters';

  type ChartMetric = 'cost' | 'tokens';
  type TokenMode = 'mix' | 'total';

  interface Props extends UsageFilters {
    data: Usage | null;
    filterOptions?: Usage['filters'];
    requestPage: Pick<Usage, 'requests' | 'nextCursor'> | null;
    loadingScope: 'overview' | 'requests' | null;
    online: boolean;
    overviewError: DashboardProblem | null;
    requestError: DashboardProblem | null;
    updatedAt: string;
    canPrevious: boolean;
    pageNumber: number;
    chartMetric?: ChartMetric;
    update: UsageFilterChange;
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
    range, source, client, model, reasoning, bucket, outcome, canPrevious, pageNumber, chartMetric = $bindable('tokens'), update,
    clearFilters, canUndoReset, undoFilters, focusRequests, onRequestFocus, refresh, retryRequests, onPrevious, onNext
  }: Props = $props();

  let tokenMode = $state<TokenMode>('mix');
  let hideUnreported = $state(false);
  let showAllModels = $state(false);
  let showAllClients = $state(false);
  let resetStatus = $state('');

  let scoped = $derived(Boolean(source.length || client.length || model.length || reasoning.length || bucket || outcome));
  let loading = $derived(loadingScope !== null);
  let firstRun = $derived(data?.summary.requestCount === 0 && !data.filters.clients.length);
  let showFilters = $derived(!firstRun || scoped || range !== '24h' || canUndoReset);
  let resetCommand = $derived(canUndoReset ? 'Undo reset' : 'Reset filters');
  let bucketLabel = $derived(bucket ? range === '24h' ? formatTimeWindow(bucket, 1) : formatUTCDay(bucket) + ' UTC' : '');
  const percentage = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 });
  const wholePercent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 0 });

  const sourceOptions = [
    { value: 'api', label: 'API providers' },
    { value: 'codex', label: 'Codex subscription' }
  ];

  const reasoningLabel = (id: string) => id === UNREPORTED_REASONING ? 'Not reported' : id;

  const reasoningColors: Record<string, string> = {
    max: 'var(--cache)',
    xhigh: 'var(--cache)',
    high: 'var(--api-light)',
    medium: 'var(--cache-write)',
    adaptive: 'var(--cache-write)',
    low: 'var(--output)',
    minimal: 'var(--output)',
    none: 'var(--input)'
  };
  let chartTitle = $derived(chartMetric === 'tokens' ? 'Tokens over time' : 'Usage value over time');

  let clientOptions = $derived(withSelectedOptions(
    (filterOptions?.clients ?? []).map(({ id, name }) => ({ value: id, label: name })), client, (id) => `Unknown client (${id})`
  ));
  let modelOptions = $derived(withSelectedOptions(
    (filterOptions?.models ?? []).map((name) => ({ value: name, label: name })), model
  ));
  let reasoningOptions = $derived(withSelectedOptions(
    (filterOptions?.reasoning ?? []).map((id) => ({ value: id, label: reasoningLabel(id) })), reasoning, reasoningLabel
  ));
  let windowOptions = $derived([
    { value: '', label: 'All time windows' },
    ...withSelectedOptions((data?.buckets ?? []).map(({ start }) => ({
      value: start, label: range === '24h' ? formatDateTime(start) : formatUTCDay(start) + ' UTC'
    })), bucket ? [bucket] : [], () => bucketLabel)
  ]);

  let totalTokens = $derived(data ? tokens(data.summary) : 0);
  let tokenBreakdown = $derived.by(() => {
    if (!data || totalTokens <= 0) return null;
    const uncached = uncachedInput(data.summary);
    const cached = data.summary.cachedInputTokens;
    const cacheWrite = data.summary.cacheWriteTokens ?? 0;
    const output = data.summary.outputTokens;
    const inputTotal = data.summary.inputTokens;
    return {
      cacheHit: inputTotal > 0 ? wholePercent.format(cached / inputTotal) : '0%',
      segments: [
        { key: 'cache', label: 'Cached', pct: cached / totalTokens, color: 'var(--cache)' },
        { key: 'input', label: 'Uncached', pct: uncached / totalTokens, color: 'var(--input)' },
        { key: 'cache-write', label: 'Cache writes', pct: cacheWrite / totalTokens, color: 'var(--cache-write)' },
        { key: 'output', label: 'Output', pct: output / totalTokens, color: 'var(--output)' }
      ].filter((segment) => segment.pct > 0)
    };
  });

  let reasoningGroups = $derived(data?.groups.reasoning ?? []);
  let reportedReasoning = $derived(
    reasoningGroups
      .filter((group) => group.id !== UNREPORTED_REASONING)
      .slice()
      .sort((a, b) => metricValue(b) - metricValue(a) || b.tokens - a.tokens || a.name.localeCompare(b.name))
  );
  let unreportedReasoning = $derived(reasoningGroups.find((group) => group.id === UNREPORTED_REASONING) ?? null);
  let showUnreportedRow = $derived(
    Boolean(unreportedReasoning) && (!hideUnreported || reasoning.includes(UNREPORTED_REASONING))
  );

  let reasoningCoverage = $derived.by(() => {
    if (!data || data.summary.requestCount === 0) return null;
    const totalReqs = data.summary.requestCount;
    const unreportedReqs = unreportedReasoning?.requestCount ?? 0;
    const reportedReqs = Math.max(0, totalReqs - unreportedReqs);
    const byCount = reportedReasoning
      .slice()
      .sort((a, b) => (b.requestCount ?? 0) - (a.requestCount ?? 0) || a.name.localeCompare(b.name));
    const segments = [
      ...byCount.map((group) => ({
        id: group.id,
        label: group.name,
        pct: (group.requestCount ?? 0) / totalReqs,
        color: reasoningColors[group.id] ?? 'var(--api-light)',
        unreported: false
      })),
      ...(unreportedReqs > 0 ? [{
        id: UNREPORTED_REASONING,
        label: 'N/R',
        pct: unreportedReqs / totalReqs,
        color: '',
        unreported: true
      }] : [])
    ].filter((segment) => segment.pct > 0);
    const legend = [
      ...segments.filter((segment) => !segment.unreported).slice(0, 3),
      ...segments.filter((segment) => segment.unreported)
    ];
    return {
      reportedShare: wholePercent.format(reportedReqs / totalReqs),
      reportedReqs,
      unreportedReqs,
      segments,
      legend
    };
  });

  let reasoningChartMax = $derived.by(() => {
    const visible = showUnreportedRow && unreportedReasoning ? [...reportedReasoning, unreportedReasoning] : reportedReasoning;
    return Math.max(0, ...visible.map(metricValue));
  });

  let reasoningChartTotal = $derived.by(() => {
    const visible = showUnreportedRow && unreportedReasoning ? [...reportedReasoning, unreportedReasoning] : reportedReasoning;
    return visible.reduce((sum, group) => sum + metricValue(group), 0);
  });

  function metricValue(group: UsageGroup) {
    if (chartMetric === 'cost' && data && data.summary.estimatedCost > 0) return group.estimatedCost;
    return group.tokens;
  }

  function barWidth(value: number, max: number) {
    if (max <= 0 || value <= 0) return '0%';
    return Math.max(2.5, Math.min(100, (value / max) * 100)).toFixed(1) + '%';
  }

  function withSelectedOptions(options: { value: string; label: string }[], selected: string[], fallback?: (value: string) => string) {
    const available = new Set(options.map(({ value }) => value));
    const missing = selected.filter((value) => !available.has(value)).map((value) => ({ value, label: fallback?.(value) ?? value }));
    return [...missing, ...options];
  }

  function share(value: number, total: number) {
    return total > 0 ? percentage.format(value / total) : '—';
  }

  function formatMix(mix?: ReasoningMixEntry[]) {
    if (!mix?.length) return '';
    if (mix.length === 1 && mix[0].level === UNREPORTED_REASONING) return 'Not reported';
    return mix
      .slice(0, 3)
      .map(({ level, share: portion }) => `${level === UNREPORTED_REASONING ? '—' : level} ${wholePercent.format(portion)}`)
      .join(' · ');
  }

  function isFullyUnreported(group: UsageGroup) {
    if (group.reasoningMix?.length === 1 && group.reasoningMix[0].level === UNREPORTED_REASONING) return true;
    return group.transport === 'OTLP';
  }

  function selectGroup(group: UsageGroup, filter: 'model' | 'client' | 'reasoning') {
    const control = document.getElementById(filter + '-filter')?.querySelector<HTMLElement>('[role="combobox"]');
    control?.focus({ preventScroll: true });
    control?.scrollIntoView({ block: 'center' });
    const selected = filter === 'model' ? model : filter === 'client' ? client : reasoning;
    update(filter, selected.includes(group.id) ? selected.filter((id) => id !== group.id) : [...selected, group.id]);
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

  function changeTokenMode(value: string | null) {
    if (value === 'mix' || value === 'total') tokenMode = value;
  }

  function retryOverview() {
    const target = document.getElementById(data ? 'refresh-usage' : 'usage-title');
    target?.focus({ preventScroll: true });
    refresh();
  }
</script>

{#snippet breakdown(groups: UsageGroup[], filter: 'model' | 'client', expanded: boolean, toggleExpanded: () => void, totalTok: number, totalCost: number)}
  {@const heading = filter === 'model' ? 'By model' : 'By client'}
  {@const ranked = groups.slice().sort((a, b) => b.estimatedCost - a.estimatedCost || b.tokens - a.tokens || a.name.localeCompare(b.name))}
  {@const previewLimit = 5}
  {@const visible = expanded ? ranked : ranked.slice(0, previewLimit)}
  {@const overflow = ranked.slice(previewLimit)}
  {@const overflowCost = overflow.reduce((sum, item) => sum + item.estimatedCost, 0)}
  {@const overflowTokens = overflow.reduce((sum, item) => sum + item.tokens, 0)}
  {@const overflowRequests = overflow.reduce((sum, item) => sum + (item.requestCount ?? 0), 0)}
  {@const overflowPricedRequests = overflow.reduce((sum, item) => sum + (item.pricedRequestCount ?? item.requestCount ?? 0), 0)}
  {@const overflowPriced = overflow.some((item) => item.pricedRequestCount !== 0)}
  {@const overflowCoverage = partialPricing({ requestCount: overflowRequests, pricedRequestCount: overflowPricedRequests })}
  {@const displayedValues = [
    ...visible.map((item) => totalCost > 0 ? item.estimatedCost : item.tokens),
    ...(!expanded && overflow.length > 0 ? [totalCost > 0 ? overflowCost : overflowTokens] : [])
  ]}
  {@const maxMetric = Math.max(0, ...displayedValues)}
  {@const selected = filter === 'model' ? model : client}
  {@const tableId = filter + '-breakdown-table'}
  <section class="breakdown-panel" aria-labelledby={filter + '-breakdown-title'}>
    <div class="breakdown-panel-head">
      <h3 id={filter + '-breakdown-title'}>{heading}</h3>
      <span class="muted breakdown-meta">
        {#if ranked.length > previewLimit && !expanded}
          Top {previewLimit} of {count(ranked.length)} · click to filter
        {:else}
          {count(ranked.length)} {ranked.length === 1 ? filter : filter + 's'} · click to filter
        {/if}
      </span>
    </div>
    {#if ranked.length}
      <table id={tableId} class="breakdown-table">
        <thead>
          <tr>
            <th scope="col">{filter === 'model' ? 'Model' : 'Client'}</th>
            <th scope="col">Usage value</th>
            <th scope="col">Tokens</th>
          </tr>
        </thead>
        <tbody>
          {#each visible as group (group.id)}
            {@const priced = group.pricedRequestCount !== 0}
            {@const coverage = partialPricing(group)}
            {@const active = selected.includes(group.id)}
            {@const hint = filter === 'model' ? formatMix(group.reasoningMix) : (group.transport ?? '')}
            {@const unreported = isFullyUnreported(group)}
            <tr class:is-active={active}>
              <th scope="row">
                <div class="rank-top">
                  {#if group.id}
                    <Button class="breakdown-filter" title={group.name + ' (Enter or Space)'}
                      aria-label={'Filter by ' + filter + ' ' + group.name} aria-keyshortcuts="Enter Space"
                      aria-pressed={active} onclick={() => selectGroup(group, filter)} onkeydown={guardEnter}>
                      {group.name}
                    </Button>
                  {:else}
                    <span class="rank-plain-name">{group.name}</span>
                  {/if}
                  {#if hint}<span class="rank-hint">{hint}</span>{/if}
                </div>
                <div class="rank-bar-track" aria-hidden="true">
                  <span
                    class="rank-bar-fill"
                    class:hatched-fill={unreported}
                    class:client-bar={filter === 'client' && !unreported}
                    style:width={barWidth(totalCost > 0 ? group.estimatedCost : group.tokens, maxMetric)}
                  ></span>
                </div>
              </th>
              <td>
                <strong>{priced ? money(group.estimatedCost) : 'Unpriced'}</strong>
                {#if priced}<span>{share(group.estimatedCost, totalCost)}</span>{/if}
                {#if coverage}<span>{coverage}</span>{/if}
              </td>
              <td>
                <strong>{compactCount(group.tokens)}</strong>
                <span>{share(group.tokens, totalTok)}</span>
              </td>
            </tr>
          {/each}

          {#if overflow.length > 0 && !expanded}
            <tr class="rank-other">
              <th scope="row">
                <div class="rank-top">
                  <span class="muted">Other ({count(overflow.length)} {overflow.length === 1 ? filter : filter + 's'})</span>
                </div>
                <div class="rank-bar-track" aria-hidden="true">
                  <span class="rank-bar-fill other-bar" style:width={barWidth(totalCost > 0 ? overflowCost : overflowTokens, maxMetric)}></span>
                </div>
              </th>
              <td>
                <strong>{overflowPriced ? money(overflowCost) : 'Unpriced'}</strong>
                {#if overflowPriced}<span>{share(overflowCost, totalCost)}</span>{/if}
                {#if overflowCoverage}<span>{overflowCoverage}</span>{/if}
              </td>
              <td>
                <strong>{compactCount(overflowTokens)}</strong>
                <span>{share(overflowTokens, totalTok)}</span>
              </td>
            </tr>
          {/if}
        </tbody>
      </table>
      {#if ranked.length > previewLimit}
        <div class="breakdown-footer">
          <Button
            class="secondary-button compact-button"
            aria-expanded={expanded}
            aria-controls={tableId}
            aria-keyshortcuts="Enter Space"
            title={(expanded ? 'Show top ' + previewLimit : 'Show all ' + count(ranked.length)) + ' (Enter or Space)'}
            onclick={toggleExpanded}
            onkeydown={guardEnter}
          >
            {expanded ? 'Show top ' + previewLimit : 'Show all ' + count(ranked.length)}
          </Button>
        </div>
      {/if}
    {:else}
      <p class="muted breakdown-note">No usage by {filter} in this view.</p>
    {/if}
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

{#snippet overviewFilter(id: string, label: string, placeholder: string, value: string[], options: { value: string; label: string }[], onChange: (value: string[]) => void)}
  <div class="filter-field" {id}>
    <span aria-hidden="true">{label}</span>
    <Filter multiple {placeholder} {label} {value} items={options} {onChange} />
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
    {@render overviewFilter('source-filter', 'Source', 'All sources', source, sourceOptions, (value) => update('source', value))}
    {@render overviewFilter('client-filter', 'Client', 'All clients', client, clientOptions, (value) => update('client', value))}
    {@render overviewFilter('model-filter', 'Model', 'All models', model, modelOptions, (value) => update('model', value))}
    {#if reasoningOptions.length || reasoning.length}
      {@render overviewFilter('reasoning-filter', 'Reasoning', 'All reasoning', reasoning, reasoningOptions, (value) => update('reasoning', value))}
    {/if}
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
      <div class="loading-metrics" aria-hidden="true"><span></span><span></span><span></span></div>
      <div class="loading-chart" aria-hidden="true"></div>
      <div class="loading-table" aria-hidden="true"></div>
    </div>
  {/if}
{:else if data.summary.requestCount === 0}
  <section class="usage-empty" aria-labelledby="empty-usage-title">
    {#if firstRun}
      <h2 id="empty-usage-title" tabindex="-1">No clients connected</h2>
      <p>Create a client to get a proxy key and set up your first request.</p>
      <a class="primary-button" href="/clients" title="Connect a client (Enter)" aria-keyshortcuts="Enter" onkeydown={guardEnter}>Connect a client</a>
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
      {#if !scoped}<a class="secondary-button" href="/clients">View clients</a>{/if}
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
      <strong title={count(totalTokens)}>{compactCount(totalTokens)}</strong>
      <span class="metric-caption">
        {count(data.summary.requestCount)} {data.summary.requestCount === 1 ? 'request' : 'requests'}{#if tokenBreakdown}{' · '}{tokenBreakdown.cacheHit} cache hit{/if}
      </span>
      {#if tokenBreakdown}
        <div class="kpi-bar" aria-hidden="true">
          {#each tokenBreakdown.segments as seg (seg.key)}
            <span style:width={(seg.pct * 100).toFixed(1) + '%'} style:background={seg.color}></span>
          {/each}
        </div>
        <div class="kpi-legend">
          {#each tokenBreakdown.segments as seg (seg.key)}
            <span><i style:background={seg.color} aria-hidden="true"></i>{seg.label} {wholePercent.format(seg.pct)}</span>
          {/each}
        </div>
      {/if}
    </section>

    {#if reasoningCoverage}
      <section class="metric" aria-label="Reasoning coverage">
        <div class="metric-label">Reasoning coverage</div>
        <strong>{reasoningCoverage.reportedShare} <span class="metric-unit">reported</span></strong>
        <span class="metric-caption">
          {count(reasoningCoverage.reportedReqs)} reported · {count(reasoningCoverage.unreportedReqs)} not reported
        </span>
        {#if reasoningCoverage.segments.length}
          <div class="kpi-bar" aria-hidden="true">
            {#each reasoningCoverage.segments as seg (seg.id)}
              <span
                class:hatched-fill={seg.unreported}
                style:width={(seg.pct * 100).toFixed(1) + '%'}
                style:background={seg.unreported ? undefined : seg.color}
              ></span>
            {/each}
          </div>
          <div class="kpi-legend">
            {#each reasoningCoverage.legend as seg (seg.id)}
              <span><i class:hatched-fill={seg.unreported} style:background={seg.unreported ? undefined : seg.color} aria-hidden="true"></i>{seg.label} {wholePercent.format(seg.pct)}</span>
            {/each}
          </div>
        {/if}
      </section>
    {/if}
  </div>

  <div class="analytics-split">
    <div class="timeline">
      <header class="section-head chart-heading">
        <h2 id="chart-heading">{chartTitle}</h2>
        <div class="chart-controls">
          <SegmentedControl class="segmented" aria-label="Chart metric" value={chartMetric} onValueChange={changeChartMetric}>
            <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
            <SegmentedControlItem value="cost">Usage value</SegmentedControlItem>
          </SegmentedControl>
          {#if chartMetric === 'tokens'}
            <SegmentedControl class="segmented" aria-label="Token bar style" value={tokenMode} onValueChange={changeTokenMode}>
              <SegmentedControlItem value="mix">Token mix</SegmentedControlItem>
              <SegmentedControlItem value="total">Total</SegmentedControlItem>
            </SegmentedControl>
          {/if}
        </div>
      </header>
      <section class="chart-section" aria-labelledby="chart-heading">
        <header>
          {#if chartMetric === 'tokens' && tokenMode === 'mix'}
            <div class="legend">
              {#each tokenSeries as series (series.key)}
                <span><i class={series.key + '-color'} data-series={series.key} aria-hidden="true"></i>{series.label}</span>
              {/each}
            </div>
          {:else if chartMetric === 'cost' && data.buckets.some(unpricedBucket)}
            <div class="legend">
              <span><i class="unpriced-mark" aria-hidden="true"></i>Unpriced window</span>
            </div>
          {/if}
        </header>
        {#key chartMetric + ':' + tokenMode}
          <TimeChart
            buckets={data.buckets}
            kind={chartMetric}
            stackedTokens={tokenMode === 'mix'}
            {range}
            selection={bucket}
            onSelect={(value) => update('bucket', bucket === value ? '' : value)}
          />
        {/key}
      </section>
      <div class="chart-actions">
        <div class="filter-field" id="request-window-filter">
          <span aria-hidden="true">Request time window</span>
          <Filter label="Request time window" value={bucket} items={windowOptions} onChange={(value: string) => update('bucket', value)} />
        </div>
        {@render chartSelection()}
      </div>
    </div>

    <section class="reasoning-panel" aria-labelledby="reasoning-panel-title">
      <header class="section-head reasoning-head">
        <h2 id="reasoning-panel-title">Reasoning levels</h2>
        {#if unreportedReasoning && !reasoning.includes(UNREPORTED_REASONING)}
          <Toggle class="secondary-button compact-button hide-unreported-toggle" bind:pressed={hideUnreported}>
            Hide unreported
          </Toggle>
        {/if}
      </header>
      <p class="muted reasoning-sub">
        Ranked by {chartMetric === 'cost' && data.summary.estimatedCost > 0 ? 'usage value' : 'tokens'} · click any level to filter
      </p>

      {#if reportedReasoning.length || showUnreportedRow}
        <div class="hbar-list">
          {#each reportedReasoning as group (group.id)}
            {@const active = reasoning.includes(group.id)}
            {@const val = metricValue(group)}
            <Button
              class="hbar-row"
              aria-pressed={active}
              aria-label={'Filter by reasoning ' + group.name}
              onclick={() => selectGroup(group, 'reasoning')}
              onkeydown={guardEnter}
            >
              <span class="hbar-label">
                <span class="reasoning-pill" data-level={group.id}>{group.name}</span>
              </span>
              <span class="hbar-track" aria-hidden="true">
                <span class="hbar-fill" style:width={barWidth(val, reasoningChartMax)} style:background={reasoningColors[group.id] ?? 'var(--api-light)'}></span>
              </span>
              <span class="hbar-nums">
                <strong>
                  {chartMetric === 'cost' && data.summary.estimatedCost > 0
                    ? (group.pricedRequestCount === 0 ? 'Unpriced' : money(group.estimatedCost))
                    : compactCount(group.tokens)}
                </strong>
                <span class="muted">
                  {share(val, reasoningChartTotal)} · {count(group.requestCount ?? 0)} req
                </span>
              </span>
            </Button>
          {/each}

          {#if !reportedReasoning.length && showUnreportedRow}
            <p class="muted reasoning-empty-reported">No requests with reported reasoning level in this view.</p>
          {/if}

          {#if showUnreportedRow && unreportedReasoning}
            {@const active = reasoning.includes(UNREPORTED_REASONING)}
            {@const val = metricValue(unreportedReasoning)}
            <div class="hbar-divider" aria-hidden="true"><span>Unreported (OTLP / legacy)</span></div>
            <Button
              class="hbar-row"
              aria-pressed={active}
              aria-label="Filter by reasoning Not reported"
              onclick={() => selectGroup(unreportedReasoning, 'reasoning')}
              onkeydown={guardEnter}
            >
              <span class="hbar-label">
                <span class="reasoning-pill" data-level="unreported">Not reported</span>
              </span>
              <span class="hbar-track" aria-hidden="true">
                <span class="hbar-fill hatched-fill" style:width={barWidth(val, reasoningChartMax)}></span>
              </span>
              <span class="hbar-nums">
                <strong class="muted">
                  {chartMetric === 'cost' && data.summary.estimatedCost > 0
                    ? (unreportedReasoning.pricedRequestCount === 0 ? 'Unpriced' : money(unreportedReasoning.estimatedCost))
                    : compactCount(unreportedReasoning.tokens)}
                </strong>
                <span class="muted">
                  {share(val, reasoningChartTotal)} · {count(unreportedReasoning.requestCount ?? 0)} req
                </span>
              </span>
            </Button>
          {/if}
        </div>
      {:else}
        <div class="reasoning-hidden-empty">
          <p class="muted">All matching requests have unreported reasoning levels.</p>
          {#if hideUnreported}
            <Button class="secondary-button compact-button" onclick={() => { hideUnreported = false; }} onkeydown={guardEnter}>
              Show unreported
            </Button>
          {/if}
        </div>
      {/if}
    </section>
  </div>

  <section class="breakdown" aria-labelledby="breakdown-title">
    <header class="section-head"><h2 id="breakdown-title">Breakdown by model &amp; client</h2></header>
    <div class="breakdown-grid">
      {@render breakdown(data.groups.models, 'model', showAllModels, () => { showAllModels = !showAllModels; }, totalTokens, data.summary.estimatedCost)}
      {@render breakdown(data.groups.clients, 'client', showAllClients, () => { showAllClients = !showAllClients; }, totalTokens, data.summary.estimatedCost)}
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
  .metrics, .analytics-split, .breakdown { display: grid; border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .filters { align-items: flex-end; }
  .filters :global(.requests-jump) { margin-inline-start: auto; }
  .filter-field { display: grid; gap: 4px; min-inline-size: 0; }
  .filter-field > span { color: var(--muted); font-size: 12px; }
  .usage-empty { border-block-start: 1px solid var(--line); padding-block: var(--section-space); }
  .usage-empty h2 { margin-block-end: 6px; scroll-margin-block-start: 8px; }
  .usage-empty p { max-inline-size: 55ch; color: var(--muted); }
  .usage-empty a { text-decoration: none; }
  .usage-loading { display: grid; gap: var(--gap); border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .loading-metrics { display: grid; gap: var(--section-space); grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .loading-metrics span, .loading-chart, .loading-table { border-radius: 6px; background: var(--surface); }
  .loading-metrics span { min-block-size: 64px; }
  .loading-chart { min-block-size: 230px; }
  .loading-table { min-block-size: 190px; }

  .metrics { grid-template-columns: minmax(0, 1fr) minmax(0, 1.15fr) minmax(0, 1.25fr); padding-block-end: 4px; }
  .metric { display: flex; flex-direction: column; align-items: flex-start; padding-inline-end: var(--section-space); min-inline-size: 0; }
  .metric + .metric { border-inline-start: 1px solid var(--line); padding-inline-start: var(--section-space); }
  .metric:last-child { padding-inline-end: 0; }
  .metric-label { color: var(--muted); font-size: 12px; }
  .metric strong { display: block; margin: 4px 0 2px; font-size: clamp(22px, 2.8vw, 32px); font-weight: 620; letter-spacing: -.035em; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .metric-unit { font-size: 15px; font-weight: 500; color: var(--muted); letter-spacing: -.01em; }
  .metric-caption { color: var(--muted); font-size: 12px; }
  .cost-explanation { margin-block-start: 4px; }
  .kpi-bar { inline-size: 100%; block-size: 6px; border-radius: 3px; overflow: hidden; display: flex; gap: 1.5px; margin-block-start: 8px; background: var(--surface-raised); }
  .kpi-bar span { display: block; block-size: 100%; }
  .kpi-legend { display: flex; flex-wrap: wrap; gap: 4px 10px; margin-block-start: 6px; font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .kpi-legend span { display: inline-flex; align-items: center; gap: 4px; }
  .kpi-legend i { inline-size: 7px; block-size: 7px; border-radius: 2px; flex: none; }

  .analytics-split { grid-template-columns: minmax(0, 1.48fr) minmax(0, 1fr); gap: 26px; align-items: start; }
  .timeline { min-inline-size: 0; }
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

  .reasoning-panel { border-inline-start: 1px solid var(--line); padding-inline-start: 24px; min-inline-size: 0; }
  .reasoning-head { padding-block-end: 6px; }
  .reasoning-panel :global(.hide-unreported-toggle[data-pressed]) { background: var(--nav-current); border-color: var(--selected-border); color: var(--nav-current-text); }
  .reasoning-sub { font-size: 12px; margin: 0 0 12px; }
  .hbar-list { display: flex; flex-direction: column; gap: 6px; }
  .reasoning-panel :global(.hbar-row) {
    display: grid;
    grid-template-columns: 92px minmax(0, 1fr) auto;
    align-items: center;
    gap: 10px;
    inline-size: 100%;
    padding: 5px 6px;
    border-radius: 6px;
    border: 1px solid transparent;
    background: transparent;
    text-align: start;
    cursor: pointer;
  }
  .reasoning-panel :global(.hbar-row:hover) { background: var(--hover); }
  .reasoning-panel :global(.hbar-row[aria-pressed="true"]) { background: var(--nav-current); border-color: var(--selected-border); }
  .hbar-label { display: flex; align-items: center; }
  .hbar-track { block-size: 13px; border-radius: 3px; background: var(--surface); overflow: hidden; }
  .hbar-fill { display: block; block-size: 100%; border-radius: 3px; }
  .hbar-nums { text-align: end; font-variant-numeric: tabular-nums; line-height: 1.25; white-space: nowrap; }
  .hbar-nums strong { display: block; font-size: 12px; font-weight: 580; color: var(--text); }
  .hbar-nums span { display: block; font-size: 11px; }
  .hbar-divider { display: flex; align-items: center; gap: 8px; margin: 4px 0 2px; font-size: 11px; color: var(--muted); }
  .hbar-divider::after { content: ''; flex: 1; block-size: 1px; background: var(--line); }
  .reasoning-empty-reported { font-size: 12px; margin: 2px 0 6px; }
  .reasoning-hidden-empty { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 10px 0; font-size: 12px; }
  .reasoning-hidden-empty p { margin: 0; }

  .breakdown-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 28px; }
  .breakdown-panel { min-inline-size: 0; }
  .breakdown-panel-head { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; margin-block-end: 8px; }
  .breakdown-panel h3 { font-size: 13px; margin: 0; }
  .breakdown-meta { font-size: 11px; }
  .breakdown-table { inline-size: 100%; border-collapse: collapse; table-layout: fixed; }
  .breakdown-table :is(th, td) { padding: 7px 0 7px 8px; border-block-start: 1px solid var(--line); text-align: end; vertical-align: middle; }
  .breakdown-table thead th { border-block-start: 0; padding-block-start: 0; padding-block-end: 6px; font-size: 11px; color: var(--muted); font-weight: 550; }
  .breakdown-table tbody tr:last-child :is(th, td) { border-block-end: 1px solid var(--line); }
  .breakdown-table tr :is(th, td):first-child { inline-size: 56%; padding-inline-start: 0; text-align: start; font-weight: 550; }
  .breakdown-table tr.is-active :is(th, td) { background: var(--table-hover); }
  .breakdown-table td { font-size: 12px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; line-height: 1.28; }
  .breakdown-table td strong { display: block; font-weight: 560; }
  .breakdown-table td span { display: block; font-size: 11px; color: var(--muted); }
  .rank-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-inline-size: 0; }
  .rank-plain-name { font-size: 12px; font-weight: 550; }
  .rank-hint { font-size: 11px; color: var(--muted); font-weight: 450; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .rank-bar-track { block-size: 4px; border-radius: 2px; background: var(--surface-raised); margin-block-start: 5px; overflow: hidden; }
  .rank-bar-fill { display: block; block-size: 100%; border-radius: 2px; background: var(--api-light); }
  .rank-bar-fill.client-bar { background: var(--codex-light); }
  .rank-bar-fill.other-bar { background: var(--strong-line); }
  .breakdown-panel :global(.breakdown-filter) { text-align: start; font: inherit; font-weight: 560; font-size: 12px; color: inherit; background: none; border: 0; padding: 2px 4px; margin-inline-start: -4px; border-radius: 4px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-inline-size: 100%; }
  .breakdown-panel :global(.breakdown-filter:hover) { background: var(--hover); }
  .breakdown-panel :global(.breakdown-filter[aria-pressed="true"]) { box-shadow: inset 0 0 0 1px var(--selected-border); background: var(--nav-current); color: var(--nav-current-text); }
  .breakdown-footer { margin-block-start: 8px; }
  .breakdown-note { margin: 7px 0 0; font-size: 12px; }

  @media (pointer: coarse) {
    .breakdown-panel :global(.breakdown-filter), .reasoning-panel :global(.hbar-row) { min-block-size: var(--touch-target); }
  }
  @media (forced-colors: active) {
    .timeline {
      --input: CanvasText;
      --cache: repeating-linear-gradient(135deg, CanvasText 0 1px, Canvas 1px 3px);
      --cache-write: repeating-linear-gradient(0deg, CanvasText 0 1px, Canvas 1px 3px);
      --output: repeating-linear-gradient(90deg, CanvasText 0 1px, Canvas 1px 3px);
    }
    .legend i[data-series], .hatched-fill { forced-color-adjust: none; }
    .hatched-fill { background: repeating-linear-gradient(-45deg, CanvasText 0 2px, Canvas 2px 4px); border: 1px solid CanvasText; }
    .breakdown-panel :global(.breakdown-filter[aria-pressed="true"]),
    .reasoning-panel :global(.hbar-row[aria-pressed="true"]) { outline: 1px solid Highlight; }
  }
  @media (max-width: 980px) {
    .metrics { grid-template-columns: 1fr; gap: 14px; }
    .metric { padding-inline: 0; }
    .metric + .metric { border-inline-start: 0; border-block-start: 1px solid var(--line); padding-inline-start: 0; padding-block-start: 14px; }
    .analytics-split { grid-template-columns: minmax(0, 1fr); gap: 20px; }
    .reasoning-panel { border-inline-start: 0; border-block-start: 1px solid var(--line); padding-inline-start: 0; padding-block-start: 18px; }
  }
  @media (max-width: 720px) {
    .breakdown-grid { grid-template-columns: minmax(0, 1fr); gap: 22px; }
    .chart-actions :global(.filter-trigger) { min-inline-size: 0; max-inline-size: none; }
    .chart-actions :global(.filter-trigger), .chart-actions :global(.secondary-button), .filters :global(button) { min-block-size: 44px; }
    .chart-selection :global(.icon-button) { inline-size: 36px; block-size: 36px; }
    .loading-metrics { grid-template-columns: minmax(0, 1fr); gap: 8px; }
    .loading-chart { min-block-size: 240px; }
  }
</style>
