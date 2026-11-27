<script lang="ts">
  import { browser } from '$app/env';
  import CalendarRangeIcon from '@lucide/svelte/icons/calendar-range';
  import CheckIcon from '@lucide/svelte/icons/check';
  import RefreshCwIcon from '@lucide/svelte/icons/refresh-cw';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Field, FieldLabel } from '@ui-sdk/svelte/field';
  import { Input } from '@ui-sdk/svelte/input';
  import { Popover, PopoverClose, PopoverContent, PopoverDescription, PopoverTitle, PopoverTrigger } from '@ui-sdk/svelte/popover';
  import { SegmentedControl, SegmentedControlItem } from '@ui-sdk/svelte/segmented-control';
  import { Toggle } from '@ui-sdk/svelte/toggle';
  import { UNREPORTED_REASONING, type DashboardProblem, type ReasoningMixEntry, type Usage, type UsageGroup } from './api';
  import { compactCount, compactMoney, compactPartialPricing, count, formatDateTime, formatDateTimeRange, formatTime, formatTimeWindow, formatUTCDay, fromDateTimeLocalValue, guardEnter, money, toDateTimeLocalValue, tokens, uncachedInput, unpricedBucket } from './format';
  import Filter from './Filter.svelte';
  import RequestList from './RequestList.svelte';
  import RecoveryNotice from './RecoveryNotice.svelte';
  import TimeChart, { tokenSeries } from './TimeChart.svelte';
  import { expectedBucketIntervalMs, hasCustomDateRange, type UsageFilterChange, type UsageFilters } from './usage-filters';

  type ChartMetric = 'cost' | 'tokens';
  type TokenMode = 'mix' | 'total';
  const HIDE_UNREPORTED_KEY = 'llm-meter-hide-unreported';

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
    setDateTimeRange: (from: string, to: string) => void;
    clearDateTimeRange: () => void;
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
    range, from, to, source, provider, client, model, reasoning, bucket, outcome, canPrevious, pageNumber, chartMetric = $bindable('tokens'), update,
    setDateTimeRange, clearDateTimeRange, clearFilters, canUndoReset, undoFilters, focusRequests, onRequestFocus, refresh, retryRequests, onPrevious, onNext
  }: Props = $props();

  function loadHideUnreported() {
    if (!browser) return false;
    try {
      return localStorage.getItem(HIDE_UNREPORTED_KEY) === 'true';
    } catch {
      return false;
    }
  }

  let tokenMode = $state<TokenMode>('mix');
  let hideUnreported = $state(loadHideUnreported());
  let breakdownSortOverride = $state<ChartMetric | null>(null);
  let showAllModels = $state(false);
  let showAllClients = $state(false);
  let resetStatus = $state('');
  let resetTick = 0;
  let customRangeOpen = $state(false);
  let draftFrom = $state('');
  let draftTo = $state('');
  let dateRangeError = $state('');

  let customDateActive = $derived(hasCustomDateRange({ from, to }));
  let customDateLabel = $derived(customDateActive ? formatDateTimeRange(from, to) : '');
  let bucketDurationMs = $derived(
    data?.bucketSeconds && data.bucketSeconds > 0
      ? data.bucketSeconds * 1000
      : expectedBucketIntervalMs({ range, from, to })
  );
  let breakdownSort = $derived<ChartMetric>(
    (breakdownSortOverride ?? chartMetric) === 'cost' && (data?.summary.estimatedCost ?? 0) > 0 ? 'cost' : 'tokens'
  );
  let isSubDay = $derived(bucketDurationMs < 86_400_000);
  let scoped = $derived(Boolean(customDateActive || source.length || provider.length || client.length || model.length || reasoning.length || bucket || outcome));
  let loading = $derived(loadingScope !== null);
  let firstRun = $derived(data?.summary.requestCount === 0 && !data.filters.clients.length);
  let showFilters = $derived(!firstRun || scoped || range !== '24h' || canUndoReset);
  let resetCommand = $derived(canUndoReset ? 'Undo reset' : 'Reset filters');
  let bucketLabel = $derived(bucket ? (isSubDay ? formatTimeWindow(bucket, bucketDurationMs / 3_600_000) : formatUTCDay(bucket) + ' UTC') : '');
  const percentage = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 });
  const wholePercent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 0 });

  $effect(() => {
    if (!browser) return;
    try {
      localStorage.setItem(HIDE_UNREPORTED_KEY, String(hideUnreported));
    } catch {
      // Storage may be blocked by the browser.
    }
  });

  const providerLabels: Record<string, string> = {
    codex: 'Codex subscription',
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
    google: 'Google',
    api: 'API'
  };

  const reasoningLabel = (id: string) => id === UNREPORTED_REASONING ? 'Not reported' : id;

  const reasoningColors: Record<string, string> = {
    max: 'var(--reasoning-xhigh-bar)',
    xhigh: 'var(--reasoning-xhigh-bar)',
    high: 'var(--reasoning-high-bar)',
    medium: 'var(--reasoning-medium-bar)',
    adaptive: 'var(--reasoning-medium-bar)',
    low: 'var(--reasoning-low-bar)',
    minimal: 'var(--reasoning-minimal-bar)',
    none: 'var(--reasoning-none-bar)'
  };
  let chartTitle = $derived(chartMetric === 'tokens' ? 'Tokens over time' : 'Usage value over time');

  let providerOptions = $derived(withSelectedOptions(
    (filterOptions?.providers ?? data?.groups.providers ?? []).map(({ id, name }) => ({ value: id, label: name })),
    provider,
    (id) => providerLabels[id] ?? id
  ));

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
      value: start, label: isSubDay ? formatDateTime(start) : formatUTCDay(start) + ' UTC'
    })), bucket ? [bucket] : [], () => bucketLabel)
  ]);

  let totalTokens = $derived(data ? tokens(data.summary) : 0);
  let cacheSavings = $derived(data?.summary.cacheSavings ?? 0);
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
  let reasoningMetricIsCost = $derived(chartMetric === 'cost' && Boolean(data && data.summary.estimatedCost > 0));
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
        color: reasoningColors[group.id] ?? 'var(--reasoning-high-bar)',
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
      ...segments.filter((segment) => !segment.unreported).slice(0, 5),
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

  let reportedReasoningMax = $derived(Math.max(0, ...reportedReasoning.map(metricValue)));
  let reportedReasoningTotal = $derived(reportedReasoning.reduce((sum, group) => sum + metricValue(group), 0));
  let reasoningOverallTotal = $derived.by(() => {
    const visible = showUnreportedRow && unreportedReasoning ? [...reportedReasoning, unreportedReasoning] : reportedReasoning;
    return visible.reduce((sum, group) => sum + metricValue(group), 0);
  });

  type BreakdownSummary = {
    ranked: UsageGroup[];
    visible: UsageGroup[];
    overflow: UsageGroup[];
    overflowCost: number;
    overflowTokens: number;
    overflowPriced: boolean;
    overflowCoverage: string;
    maxMetric: number;
    useCost: boolean;
  };

  function rankBreakdown(groups: UsageGroup[], expanded: boolean, totalCost: number, sortBy: ChartMetric): BreakdownSummary {
    const useCost = sortBy === 'cost' && totalCost > 0;
    const ranked = groups.slice().sort((a, b) =>
      useCost
        ? (b.estimatedCost - a.estimatedCost || b.tokens - a.tokens || a.name.localeCompare(b.name))
        : (b.tokens - a.tokens || b.estimatedCost - a.estimatedCost || a.name.localeCompare(b.name))
    );
    const previewLimit = 5;
    const visible = expanded ? ranked : ranked.slice(0, previewLimit);
    const overflow = ranked.slice(previewLimit);
    let overflowCost = 0;
    let overflowTokens = 0;
    let overflowRequests = 0;
    let overflowPricedRequests = 0;
    let overflowPriced = false;
    for (const item of overflow) {
      overflowCost += item.estimatedCost;
      overflowTokens += item.tokens;
      overflowRequests += item.requestCount ?? 0;
      overflowPricedRequests += item.pricedRequestCount ?? item.requestCount ?? 0;
      if (item.pricedRequestCount !== 0) overflowPriced = true;
    }
    const overflowCoverage = compactPartialPricing({ requestCount: overflowRequests, pricedRequestCount: overflowPricedRequests });
    const displayedValues = [
      ...visible.map((item) => useCost ? item.estimatedCost : item.tokens),
      ...(!expanded && overflow.length > 0 ? [useCost ? overflowCost : overflowTokens] : [])
    ];
    return {
      ranked,
      visible,
      overflow,
      overflowCost,
      overflowTokens,
      overflowPriced,
      overflowCoverage,
      maxMetric: Math.max(0, ...displayedValues),
      useCost
    };
  }

  let modelBreakdown = $derived(rankBreakdown(data?.groups.models ?? [], showAllModels, data?.summary.estimatedCost ?? 0, breakdownSort));
  let clientBreakdown = $derived(rankBreakdown(data?.groups.clients ?? [], showAllClients, data?.summary.estimatedCost ?? 0, breakdownSort));
  let providerBreakdown = $derived(rankBreakdown(data?.groups.providers ?? [], true, data?.summary.estimatedCost ?? 0, breakdownSort));

  function metricValue(group: UsageGroup) {
    if (reasoningMetricIsCost) return group.estimatedCost;
    return group.tokens;
  }

  function barWidth(value: number, max: number) {
    if (max <= 0 || value <= 0) return '0%';
    return Math.max(3, Math.min(100, (value / max) * 100)).toFixed(1) + '%';
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
      .slice(0, 4)
      .map(({ level, share: portion }) => `${level === UNREPORTED_REASONING ? 'N/R' : level} ${wholePercent.format(portion)}`)
      .join(' · ');
  }

  function selectGroup(group: UsageGroup, filter: 'model' | 'client' | 'provider' | 'reasoning') {
    const control = document.getElementById(filter + '-filter')?.querySelector<HTMLElement>('[role="combobox"]');
    control?.focus({ preventScroll: true });
    control?.scrollIntoView({ block: 'center' });
    const selected = filter === 'model' ? model : filter === 'client' ? client : filter === 'provider' ? provider : reasoning;
    update(filter, selected.includes(group.id) ? selected.filter((id) => id !== group.id) : [...selected, group.id]);
  }

  function handleBreakdownRowClick(event: MouseEvent, group: UsageGroup, filter: 'model' | 'client' | 'provider') {
    if (!group.id) return;
    if ((event.target as HTMLElement | null)?.closest('button')) return;
    if (typeof window !== 'undefined' && window.getSelection()?.toString()) return;
    selectGroup(group, filter);
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
    resetTick++;
    if (canUndoReset) {
      undoFilters();
      resetStatus = resetTick % 2 === 0 ? 'Previous filters restored.' : 'Previous filters restored.\u200B';
    } else {
      clearFilters();
      resetStatus = resetTick % 2 === 0 ? 'Filters reset.' : 'Filters reset.\u200B';
    }
  }

  function clearRequestWindow() {
    const control = document.getElementById('request-window-filter')?.querySelector<HTMLElement>('[role="combobox"]')
      ?? document.getElementById('empty-usage-title');
    control?.focus({ preventScroll: true });
    update('bucket', '');
  }

  function changeChartMetric(value: string | null) {
    if (value === 'cost' || value === 'tokens') {
      chartMetric = value;
      breakdownSortOverride = null;
    }
  }

  function changeBreakdownSort(value: string | null) {
    if (value === 'cost' || value === 'tokens') breakdownSortOverride = value;
  }

  function changeTokenMode(value: string | null) {
    if (value === 'mix' || value === 'total') tokenMode = value;
  }

  function retryOverview() {
    const target = document.getElementById(data ? 'refresh-usage' : 'usage-title');
    target?.focus({ preventScroll: true });
    refresh();
  }

  function syncDraftDateRange() {
    dateRangeError = '';
    if (customDateActive) {
      draftFrom = toDateTimeLocalValue(from);
      draftTo = toDateTimeLocalValue(to);
      return;
    }
    const now = new Date();
    const spanMs = range === '1h' ? 3_600_000 : range === '7d' ? 7 * 86_400_000 : range === '30d' ? 30 * 86_400_000 : 86_400_000;
    draftFrom = toDateTimeLocalValue(new Date(now.getTime() - spanMs).toISOString());
    draftTo = toDateTimeLocalValue(now.toISOString());
  }

  function handleDatePopoverChange(open: boolean) {
    if (open) syncDraftDateRange();
    else dateRangeError = '';
  }

  function fillQuickDay(offsetDays: number) {
    const start = new Date();
    start.setHours(0, 0, 0, 0);
    start.setDate(start.getDate() + offsetDays);
    const end = offsetDays === 0 ? new Date() : new Date(start.getTime() + 86_400_000);
    draftFrom = toDateTimeLocalValue(start.toISOString());
    draftTo = toDateTimeLocalValue(end.toISOString());
    dateRangeError = '';
  }

  function handleDateInputKeydown(event: KeyboardEvent) {
    guardEnter(event);
    if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || event.altKey || event.metaKey || event.ctrlKey || event.shiftKey) return;
    if (event.key === 'Enter') {
      event.preventDefault();
      (event.currentTarget as HTMLInputElement).form?.requestSubmit();
    }
  }

  function applyCustomDateRange(event: SubmitEvent) {
    event.preventDefault();
    const fromISO = fromDateTimeLocalValue(draftFrom);
    const toISO = fromDateTimeLocalValue(draftTo);
    if (!fromISO || !toISO) {
      dateRangeError = 'Enter both start and end date/time.';
      return;
    }
    const spanMs = Date.parse(toISO) - Date.parse(fromISO);
    if (spanMs < 60_000) {
      dateRangeError = 'End time must be at least 1 minute after start time.';
      return;
    }
    if (spanMs > 366 * 86_400_000) {
      dateRangeError = 'Custom range cannot exceed 366 days.';
      return;
    }
    dateRangeError = '';
    customRangeOpen = false;
    setDateTimeRange(fromISO, toISO);
  }
</script>

{#snippet breakdown(summary: BreakdownSummary, filter: 'model' | 'client', expanded: boolean, toggleExpanded: () => void, totalTok: number, totalCost: number)}
  {@const heading = filter === 'model' ? 'By model' : 'By client'}
  {@const previewLimit = 5}
  {@const selected = filter === 'model' ? model : client}
  {@const tableId = filter + '-breakdown-table'}
  <section class="breakdown-panel" aria-labelledby={filter + '-breakdown-title'}>
    <div class="breakdown-panel-head">
      <h3 id={filter + '-breakdown-title'}>{heading}</h3>
      <span class="muted breakdown-meta">
        {#if summary.ranked.length > previewLimit && !expanded}
          Top {previewLimit} of {count(summary.ranked.length)} · click to filter
        {:else}
          {count(summary.ranked.length)} {summary.ranked.length === 1 ? filter : filter + 's'} · click to filter
        {/if}
      </span>
    </div>
    {#if summary.ranked.length}
      <table id={tableId} class="breakdown-table">
        <thead>
          <tr>
            <th scope="col">{filter === 'model' ? 'Model' : 'Client'}</th>
            <th scope="col" class:sorted-col={summary.useCost}>Usage value</th>
            <th scope="col" class:sorted-col={!summary.useCost}>Tokens</th>
          </tr>
        </thead>
        <tbody>
          {#each summary.visible as group (group.id)}
            {@const priced = group.pricedRequestCount !== 0}
            {@const coverage = compactPartialPricing(group)}
            {@const active = selected.includes(group.id)}
            {@const hint = filter === 'model' ? formatMix(group.reasoningMix) : (group.transport ?? '')}
            <tr
              class:is-interactive={Boolean(group.id)}
              class:is-active={active}
              onclick={(event) => handleBreakdownRowClick(event, group, filter)}
            >
              <th scope="row">
                <div class="rank-name-row">
                  {#if group.id}
                    <Button
                      class={filter === 'model' ? 'breakdown-filter is-model' : 'breakdown-filter'}
                      aria-label={'Filter by ' + filter + ' ' + group.name}
                      aria-keyshortcuts="Enter Space"
                      aria-pressed={active}
                      onclick={() => selectGroup(group, filter)}
                      onkeydown={guardEnter}
                    >
                      <span class="breakdown-filter-label">{group.name}</span>
                      {#if active}<CheckIcon size={13} class="breakdown-active-check" aria-hidden="true" />{/if}
                    </Button>
                  {:else}
                    <span class="rank-plain-name" class:is-model={filter === 'model'}>{group.name}</span>
                  {/if}
                </div>
                <div class="rank-bar-track" aria-hidden="true">
                  <span
                    class="rank-bar-fill"
                    class:client-bar={filter === 'client'}
                    style:width={barWidth(summary.useCost ? group.estimatedCost : group.tokens, summary.maxMetric)}
                  ></span>
                </div>
                {#if hint}
                  <div class="rank-sub" title={hint}>{hint}</div>
                {/if}
              </th>
              <td>
                <strong title={priced ? money(group.estimatedCost) : undefined}>{priced ? compactMoney(group.estimatedCost) : 'Unpriced'}</strong>
                {#if priced}<span>{share(group.estimatedCost, totalCost)}</span>{/if}
                {#if coverage}<span class="priced-count">{coverage}</span>{/if}
              </td>
              <td>
                <strong title={count(group.tokens)}>{compactCount(group.tokens)}</strong>
                <span>{share(group.tokens, totalTok)}</span>
              </td>
            </tr>
          {/each}

          {#if summary.overflow.length > 0 && !expanded}
            <tr class="rank-other">
              <th scope="row">
                <div class="rank-name-row">
                  <span class="muted">Other ({count(summary.overflow.length)} {summary.overflow.length === 1 ? filter : filter + 's'})</span>
                </div>
                <div class="rank-bar-track" aria-hidden="true">
                  <span class="rank-bar-fill other-bar" style:width={barWidth(summary.useCost ? summary.overflowCost : summary.overflowTokens, summary.maxMetric)}></span>
                </div>
              </th>
              <td>
                <strong title={summary.overflowPriced ? money(summary.overflowCost) : undefined}>{summary.overflowPriced ? compactMoney(summary.overflowCost) : 'Unpriced'}</strong>
                {#if summary.overflowPriced}<span>{share(summary.overflowCost, totalCost)}</span>{/if}
                {#if summary.overflowCoverage}<span class="priced-count">{summary.overflowCoverage}</span>{/if}
              </td>
              <td>
                <strong title={count(summary.overflowTokens)}>{compactCount(summary.overflowTokens)}</strong>
                <span>{share(summary.overflowTokens, totalTok)}</span>
              </td>
            </tr>
          {/if}
        </tbody>
      </table>
      {#if summary.ranked.length > previewLimit}
        <div class="breakdown-footer">
          <Button
            class="secondary-button compact-button"
            aria-expanded={expanded}
            aria-controls={tableId}
            aria-keyshortcuts="Enter Space"
            title={(expanded ? 'Show top ' + previewLimit : 'Show all ' + count(summary.ranked.length)) + ' (Enter or Space)'}
            onclick={toggleExpanded}
            onkeydown={guardEnter}
          >
            {expanded ? 'Show top ' + previewLimit : 'Show all ' + count(summary.ranked.length)}
          </Button>
        </div>
      {/if}
    {:else}
      <p class="muted breakdown-note">No usage by {filter} in this view.</p>
    {/if}
  </section>
{/snippet}

{#snippet emptyBucketChip()}
  <div class="chart-selection has-selection">
    <span aria-live="polite">Requests for {bucketLabel}</span>
    <Button class="icon-button" title="Clear chart time filter (Enter)" aria-label="Clear chart time filter" aria-keyshortcuts="Enter" onclick={clearRequestWindow} onkeydown={guardEnter}>
      <XIcon size={14} aria-hidden="true" />
    </Button>
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
        <p aria-live="polite">
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
    <div class="range-controls">
      <SegmentedControl class="segmented" aria-label="Date range" value={customDateActive ? '' : range} onValueChange={(value) => { if (value) update('range', value); }}>
        <SegmentedControlItem value="1h">Last 1h</SegmentedControlItem>
        <SegmentedControlItem value="24h">Last 24h</SegmentedControlItem>
        <SegmentedControlItem value="7d">7 days</SegmentedControlItem>
        <SegmentedControlItem value="30d">30 days</SegmentedControlItem>
      </SegmentedControl>
      <div class="datetime-control" class:is-active={customDateActive}>
        <Popover bind:open={customRangeOpen} onOpenChange={handleDatePopoverChange}>
          <PopoverTrigger
            class="secondary-button datetime-trigger"
            aria-label={customDateActive ? `Custom time range: ${customDateLabel}` : 'Custom time range'}
            title={customDateActive ? `Custom time range: ${customDateLabel}` : 'Filter by custom date and time range'}
          >
            <CalendarRangeIcon size={14} aria-hidden="true" />
            <span class="datetime-label">{customDateActive ? customDateLabel : 'Custom range'}</span>
          </PopoverTrigger>
          <PopoverContent class="datetime-popover" side="bottom" align="end" sideOffset={6} collisionPadding={12}>
            <PopoverTitle class="datetime-popover-title">Custom time range</PopoverTitle>
            <PopoverDescription class="muted datetime-popover-desc">Filter all metrics, chart, breakdowns, and requests in local time.</PopoverDescription>
            <div class="datetime-quick">
              <Button type="button" class="secondary-button compact-button" onclick={() => fillQuickDay(0)} onkeydown={guardEnter}>Today</Button>
              <Button type="button" class="secondary-button compact-button" onclick={() => fillQuickDay(-1)} onkeydown={guardEnter}>Yesterday</Button>
            </div>
            <form class="datetime-form" onsubmit={applyCustomDateRange}>
              <Field class="form-field" name="from">
                <FieldLabel>From</FieldLabel>
                <Input class="text-input datetime-input" type="datetime-local" required bind:value={draftFrom} onkeydown={handleDateInputKeydown} />
              </Field>
              <Field class="form-field" name="to">
                <FieldLabel>To</FieldLabel>
                <Input class="text-input datetime-input" type="datetime-local" required bind:value={draftTo} onkeydown={handleDateInputKeydown} />
              </Field>
              {#if dateRangeError}
                <p class="field-error" role="alert">{dateRangeError}</p>
              {/if}
              <div class="datetime-actions">
                {#if customDateActive}
                  <Button type="button" class="secondary-button" onclick={() => { customRangeOpen = false; clearDateTimeRange(); }} onkeydown={guardEnter}>Clear</Button>
                {/if}
                <PopoverClose class="secondary-button">Cancel</PopoverClose>
                <Button type="submit" class="primary-button" title="Apply range (Enter)" aria-keyshortcuts="Enter" onkeydown={guardEnter}>
                  Apply range <kbd aria-hidden="true">↵</kbd>
                </Button>
              </div>
            </form>
          </PopoverContent>
        </Popover>
        {#if customDateActive}
          <Button
            class="icon-button datetime-clear"
            aria-label="Clear custom time range"
            title="Clear custom time range (Enter)"
            aria-keyshortcuts="Enter"
            onclick={clearDateTimeRange}
            onkeydown={guardEnter}
          >
            <XIcon size={14} aria-hidden="true" />
          </Button>
        {/if}
      </div>
    </div>
  {/if}
</header>

{#if showFilters}
  <div class="filters">
    {@render overviewFilter('provider-filter', 'Provider', 'All providers', provider, providerOptions, (value) => update('provider', value))}
    {@render overviewFilter('client-filter', 'Client', 'All clients', client, clientOptions, (value) => update('client', value))}
    {@render overviewFilter('model-filter', 'Model', 'All models', model, modelOptions, (value) => update('model', value))}
    {#if reasoningOptions.length || reasoning.length}
      {@render overviewFilter('reasoning-filter', 'Reasoning', 'All reasoning', reasoning, reasoningOptions, (value) => update('reasoning', value))}
    {/if}
    {#if source.length}
      <div class="legacy-source-chips" role="group" aria-label="Active source filter">
        {#each source as src (src)}
          {@const srcLabel = src === 'codex' ? 'Codex subscription' : 'API providers'}
          <span class="legacy-source-chip">
            <span>Source: {srcLabel}</span>
            <Button
              class="legacy-source-remove"
              aria-label={'Remove source filter ' + srcLabel}
              aria-keyshortcuts="Enter Space"
              title={'Remove source filter ' + srcLabel + ' (Enter or Space)'}
              onclick={() => update('source', source.filter((item) => item !== src))}
              onkeydown={guardEnter}
            >
              <XIcon size={12} aria-hidden="true" />
            </Button>
          </span>
        {/each}
      </div>
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
      {#if bucket}{@render emptyBucketChip()}{/if}
      {#if scoped}
        <p>No requests match your filters. Reset the filters or choose another date range.</p>
      {:else}
        <p>Send a request from a configured client to see its usage here.</p>
      {/if}
      <div class="empty-actions">
        {#if scoped}
          <Button class="secondary-button" onclick={resetOrUndoFilters} onkeydown={guardEnter}>{resetCommand}</Button>
        {/if}
        {#if range !== '30d'}
          <Button class="secondary-button" onclick={() => update('range', '30d')} onkeydown={guardEnter}>Show last 30 days</Button>
        {/if}
        {#if !scoped}<a class="secondary-button" href="/clients">View clients</a>{/if}
      </div>
    {/if}
  </section>
{:else}
  <div class="metrics">
    <section class="metric" aria-label="Usage value">
      <div class="metric-label">Usage value</div>
      <strong title={data.summary.pricedRequestCount === 0 ? undefined : money(data.summary.estimatedCost)}>
        {data.summary.pricedRequestCount === 0 ? 'Unpriced' : compactMoney(data.summary.estimatedCost)}
      </strong>
      {#if data.summary.pricedRequestCount != null}
        <span class="metric-caption">{count(data.summary.pricedRequestCount)} of {count(data.summary.requestCount)} {data.summary.requestCount === 1 ? 'request' : 'requests'} priced</span>
      {/if}
      {#if cacheSavings > 0}
        <span class="metric-savings" title={`Estimated savings from cached input tokens: ${money(cacheSavings)}`}>
          Saved {compactMoney(cacheSavings)} via prompt cache
        </span>
      {/if}
      <span class="metric-caption cost-explanation">Reported charges, provider list values and estimates, in USD.</span>
    </section>

    <section class="metric" aria-label="Total tokens">
      <div class="metric-label">Total tokens</div>
      <strong title={count(totalTokens)} aria-label={count(totalTokens) + ' total tokens'}>{compactCount(totalTokens)}</strong>
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
        <div class="metric-label">Reasoning coverage <span class="metric-basis">by requests</span></div>
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
        <TimeChart
          buckets={data.buckets}
          bucketSeconds={data.bucketSeconds}
          rangeStart={customDateActive ? from : undefined}
          rangeEnd={customDateActive ? to : undefined}
          kind={chartMetric}
          stackedTokens={tokenMode === 'mix'}
          {range}
          selection={bucket}
          onSelect={(value) => update('bucket', bucket === value ? '' : value)}
          onSelectRange={setDateTimeRange}
        />
      </section>
    </div>

    <section class="reasoning-panel" aria-labelledby="reasoning-panel-title">
      <header class="section-head reasoning-head">
        <h2 id="reasoning-panel-title">Reasoning levels</h2>
        {#if unreportedReasoning && !reasoning.includes(UNREPORTED_REASONING)}
          <Toggle class="secondary-button compact-button hide-unreported-toggle" bind:pressed={hideUnreported} onkeydown={guardEnter}>
            Hide unreported
          </Toggle>
        {/if}
      </header>
      <p class="muted reasoning-sub">
        By {reasoningMetricIsCost ? 'usage value' : 'tokens'}{#if showUnreportedRow && reportedReasoning.length}{' · '}reported bars scaled to top level{/if}{' · '}click to filter
      </p>

      {#if reportedReasoning.length || showUnreportedRow}
        <div class="hbar-list">
          {#each reportedReasoning as group (group.id)}
            {@const active = reasoning.includes(group.id)}
            {@const val = metricValue(group)}
            {@const numsId = 'reasoning-nums-' + group.id}
            <Button
              class="hbar-row"
              aria-pressed={active}
              aria-label={'Filter by reasoning ' + group.name}
              aria-describedby={numsId}
              onclick={() => selectGroup(group, 'reasoning')}
              onkeydown={guardEnter}
            >
              <span class="hbar-label">
                <span class="reasoning-pill" data-level={group.id}>{group.name}</span>
              </span>
              <span class="hbar-track" aria-hidden="true">
                <span class="hbar-fill" style:width={barWidth(val, reportedReasoningMax)} style:background={reasoningColors[group.id] ?? 'var(--reasoning-high-bar)'}></span>
              </span>
              <span class="hbar-nums" id={numsId}>
                <strong title={reasoningMetricIsCost && group.pricedRequestCount !== 0 ? money(group.estimatedCost) : count(group.tokens)}>
                  {reasoningMetricIsCost
                    ? (group.pricedRequestCount === 0 ? 'Unpriced' : compactMoney(group.estimatedCost))
                    : compactCount(group.tokens)}
                </strong>
                <span class="muted">
                  {share(val, showUnreportedRow ? reportedReasoningTotal : reasoningOverallTotal)}{showUnreportedRow ? ' of reported' : ''} · {count(group.requestCount ?? 0)} req
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
            <div class="hbar-divider">
              <span>Unreported (OpenTelemetry / legacy) · {share(val, reasoningOverallTotal)} of total</span>
            </div>
            <Button
              class="hbar-row"
              aria-pressed={active}
              aria-label="Filter by reasoning Not reported"
              aria-describedby="reasoning-nums-unreported"
              onclick={() => selectGroup(unreportedReasoning, 'reasoning')}
              onkeydown={guardEnter}
            >
              <span class="hbar-label">
                <span class="reasoning-pill" data-level="unreported">Not reported</span>
              </span>
              <span class="hbar-track" aria-hidden="true">
                <span class="hbar-fill hatched-fill" style:width={barWidth(val, reasoningOverallTotal)}></span>
              </span>
              <span class="hbar-nums" id="reasoning-nums-unreported">
                <strong class="muted" title={reasoningMetricIsCost && unreportedReasoning.pricedRequestCount !== 0 ? money(unreportedReasoning.estimatedCost) : count(unreportedReasoning.tokens)}>
                  {reasoningMetricIsCost
                    ? (unreportedReasoning.pricedRequestCount === 0 ? 'Unpriced' : compactMoney(unreportedReasoning.estimatedCost))
                    : compactCount(unreportedReasoning.tokens)}
                </strong>
                <span class="muted">
                  {share(val, reasoningOverallTotal)} · {count(unreportedReasoning.requestCount ?? 0)} req
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
    <header class="section-head breakdown-head">
      <h2 id="breakdown-title">Breakdown by model &amp; client</h2>
      <div class="breakdown-sort">
        <span class="muted" aria-hidden="true">Rank by</span>
        <SegmentedControl class="segmented small" aria-label="Breakdown sort" value={breakdownSort} onValueChange={changeBreakdownSort}>
          <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
          <SegmentedControlItem value="cost" disabled={data.summary.estimatedCost <= 0}>Usage value</SegmentedControlItem>
        </SegmentedControl>
      </div>
    </header>
    <div class="breakdown-grid">
      {@render breakdown(modelBreakdown, 'model', showAllModels, () => { showAllModels = !showAllModels; }, totalTokens, data.summary.estimatedCost)}
      <div class="breakdown-secondary-col">
        {@render breakdown(clientBreakdown, 'client', showAllClients, () => { showAllClients = !showAllClients; }, totalTokens, data.summary.estimatedCost)}

        {#if providerBreakdown.ranked.length}
          <section class="breakdown-panel provider-panel" aria-labelledby="provider-breakdown-title">
            <div class="breakdown-panel-head">
              <h3 id="provider-breakdown-title">By provider</h3>
              <span class="muted breakdown-meta">
                {count(providerBreakdown.ranked.length)} {providerBreakdown.ranked.length === 1 ? 'provider' : 'providers'} · click to filter
              </span>
            </div>
            <table id="provider-breakdown-table" class="breakdown-table">
              <thead>
                <tr>
                  <th scope="col">Provider</th>
                  <th scope="col" class:sorted-col={providerBreakdown.useCost}>Usage value</th>
                  <th scope="col" class:sorted-col={!providerBreakdown.useCost}>Tokens</th>
                </tr>
              </thead>
              <tbody>
                {#each providerBreakdown.ranked as group (group.id)}
                  {@const priced = group.pricedRequestCount !== 0}
                  {@const coverage = compactPartialPricing(group)}
                  {@const active = provider.includes(group.id)}
                  <tr
                    class:is-interactive={Boolean(group.id)}
                    class:is-active={active}
                    onclick={(event) => handleBreakdownRowClick(event, group, 'provider')}
                  >
                    <th scope="row">
                      <div class="rank-name-row">
                        <Button
                          class="breakdown-filter"
                          aria-label={'Filter by provider ' + group.name}
                          aria-keyshortcuts="Enter Space"
                          aria-pressed={active}
                          onclick={() => selectGroup(group, 'provider')}
                          onkeydown={guardEnter}
                        >
                          <span class="breakdown-filter-label">{group.name}</span>
                          {#if active}<CheckIcon size={13} class="breakdown-active-check" aria-hidden="true" />{/if}
                        </Button>
                      </div>
                      <div class="rank-bar-track" aria-hidden="true">
                        <span
                          class="rank-bar-fill provider-bar"
                          style:width={barWidth(providerBreakdown.useCost ? group.estimatedCost : group.tokens, providerBreakdown.maxMetric)}
                        ></span>
                      </div>
                      {#if group.transport}
                        <div class="rank-sub">{group.transport}</div>
                      {/if}
                    </th>
                    <td>
                      <strong title={priced ? money(group.estimatedCost) : undefined}>{priced ? compactMoney(group.estimatedCost) : 'Unpriced'}</strong>
                      {#if priced}<span>{share(group.estimatedCost, data.summary.estimatedCost)}</span>{/if}
                      {#if coverage}<span class="priced-count">{coverage}</span>{/if}
                    </td>
                    <td>
                      <strong title={count(group.tokens)}>{compactCount(group.tokens)}</strong>
                      <span>{share(group.tokens, totalTokens)}</span>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </section>
        {/if}
      </div>
    </div>
  </section>

  <RequestList
    requests={requestPage?.requests}
    {bucket}
    {bucketLabel}
    {windowOptions}
    onBucketChange={(value) => update('bucket', value)}
    onClearBucket={clearRequestWindow}
    {loading}
    {canPrevious}
    canNext={Boolean(requestPage?.nextCursor)}
    {outcome}
    onOutcomeChange={(value) => update('outcome', value)}
    {pageNumber}
    error={requestError}
    retry={retryRequests}
    {onPrevious}
    {onNext}
  />
{/if}

<style>
  .update-line { display: flex; align-items: center; gap: 7px; font-size: 12px; margin-block-start: 4px; }
  .update-line p { display: flex; align-items: center; gap: 6px; margin: 0; }
  .update-line :global(.refresh-button) { background: transparent; border-color: transparent; }
  .range-controls { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
  .datetime-control { display: inline-flex; align-items: center; max-inline-size: 100%; }
  .datetime-control :global(.datetime-trigger) { min-block-size: 36px; max-inline-size: min(100%, 280px); font-variant-numeric: tabular-nums; }
  .datetime-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .datetime-control.is-active :global(.datetime-trigger) {
    background: var(--nav-current);
    border-color: var(--selected-border);
    color: var(--nav-current-text);
    border-start-end-radius: 0;
    border-end-end-radius: 0;
  }
  .datetime-control :global(.datetime-clear) {
    block-size: 36px;
    inline-size: 32px;
    border-start-start-radius: 0;
    border-end-start-radius: 0;
    border-inline-start: 0;
    background: var(--nav-current);
    border-color: var(--selected-border);
    color: var(--nav-current-text);
  }
  .datetime-control :global(.datetime-clear:hover) { background: var(--option-highlighted); }
  :global(.datetime-popover) {
    inline-size: min(calc(100vw - 24px), 320px);
    padding: 14px;
    border-radius: 9px;
    border: 1px solid var(--strong-line);
    background: var(--surface-raised);
    color: var(--text);
    box-shadow: 0 14px 40px var(--popover-shadow);
    display: grid;
    gap: 10px;
  }
  :global(.datetime-popover-title) { font-size: 14px; font-weight: 620; margin: 0; }
  :global(.datetime-popover-desc) { font-size: 12px; margin: -4px 0 0; line-height: 1.4; }
  .datetime-quick { display: flex; gap: 6px; flex-wrap: wrap; }
  .datetime-form { display: grid; gap: 10px; }
  .datetime-form :global(.datetime-input) { inline-size: 100%; font-variant-numeric: tabular-nums; }
  .datetime-actions { display: flex; justify-content: flex-end; gap: 6px; margin-block-start: 2px; }
  .metrics, .analytics-split, .breakdown { display: grid; border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .filters { align-items: flex-end; }
  .filters :global(.requests-jump) { margin-inline-start: auto; }
  .filter-field { display: grid; gap: 4px; min-inline-size: 0; }
  .filter-field > span { color: var(--muted); font-size: 12px; }
  .legacy-source-chips { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .legacy-source-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-block-size: 32px;
    padding-inline: 8px 4px;
    border-radius: 6px;
    border: 1px solid var(--selected-border);
    background: var(--nav-current);
    color: var(--nav-current-text);
    font-size: 12px;
  }
  .legacy-source-chip :global(.legacy-source-remove) {
    display: grid;
    place-items: center;
    min-inline-size: 24px;
    min-block-size: 24px;
    padding: 0;
    border: 0;
    border-radius: 4px;
    background: transparent;
    cursor: pointer;
  }
  .legacy-source-chip :global(.legacy-source-remove:hover) { background: var(--option-highlighted); }
  .usage-empty { border-block-start: 1px solid var(--line); padding-block: var(--section-space); }
  .usage-empty h2 { margin-block-end: 6px; scroll-margin-block-start: 8px; }
  .usage-empty p { max-inline-size: 55ch; color: var(--muted); }
  .usage-empty a { text-decoration: none; }
  .empty-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-block-start: 10px; }
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
  .metric-label { display: flex; align-items: baseline; gap: 6px; color: var(--muted); font-size: 12px; }
  .metric-basis { font-size: 10.5px; padding: 1px 5px; border-radius: 4px; border: 1px solid var(--line); background: var(--surface); color: var(--muted); }
  .metric strong { display: block; margin: 4px 0 2px; font-size: clamp(22px, 2.8vw, 32px); font-weight: 620; letter-spacing: -.035em; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .metric-unit { font-size: 15px; font-weight: 500; color: var(--muted); letter-spacing: -.01em; }
  .metric-caption { color: var(--muted); font-size: 12px; }
  .metric-savings { margin-block-start: 4px; font-size: 11.5px; font-weight: 540; color: var(--success); font-variant-numeric: tabular-nums; }
  .cost-explanation { margin-block-start: 4px; }
  .kpi-bar { inline-size: 100%; block-size: 6px; border-radius: 3px; overflow: hidden; display: flex; gap: 1.5px; margin-block-start: 8px; background: var(--surface-raised); }
  .kpi-bar span { display: block; block-size: 100%; }
  .kpi-legend { display: flex; flex-wrap: wrap; gap: 4px 10px; margin-block-start: 6px; font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .kpi-legend span { display: inline-flex; align-items: center; gap: 4px; }
  .kpi-legend i { inline-size: 7px; block-size: 7px; border-radius: 2px; flex: none; }

  .analytics-split { grid-template-columns: minmax(0, 1.48fr) minmax(0, 1fr); gap: var(--section-space); align-items: start; }
  .timeline { min-inline-size: 0; }
  .chart-heading { flex-wrap: wrap; }
  .chart-controls { display: flex; gap: 6px; flex-wrap: wrap; }
  .chart-section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; min-inline-size: 0; }
  .chart-section header:empty { display: none; }
  .legend { display: flex; gap: 13px; align-items: center; flex-wrap: wrap; font-size: 12px; color: var(--muted); }
  .legend span { display: inline-flex; gap: 5px; align-items: center; overflow-wrap: anywhere; }
  .legend i { flex: none; width: 8px; height: 8px; border-radius: 2px; }
  .legend .unpriced-mark { block-size: 0; inline-size: 14px; border-block-end: 2px dotted var(--muted); }
  .chart-selection { display: inline-flex; gap: 6px; align-items: center; flex-wrap: wrap; color: var(--muted); font-size: 12px; margin-block-end: 8px; }
  .chart-selection :global(.icon-button) { border-color: transparent; background: transparent; }

  .reasoning-panel { border-inline-start: 1px solid var(--line); padding-inline-start: var(--section-space); min-inline-size: 0; }
  .reasoning-head { padding-block-end: 6px; }
  .reasoning-panel :global(.hide-unreported-toggle[data-pressed]) { background: var(--nav-current); border-color: var(--selected-border); color: var(--nav-current-text); }
  .reasoning-sub { font-size: 12px; margin: 0 0 12px; }
  .hbar-list { display: flex; flex-direction: column; gap: 6px; }
  .reasoning-panel :global(.hbar-row) {
    display: grid;
    grid-template-columns: minmax(5.75rem, auto) minmax(0, 1fr) auto;
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
  .hbar-divider { display: flex; align-items: center; gap: 8px; margin: 6px 0 2px; font-size: 11px; color: var(--muted); }
  .hbar-divider::after { content: ''; flex: 1; block-size: 1px; background: var(--line); }
  .reasoning-empty-reported { font-size: 12px; margin: 2px 0 6px; }
  .reasoning-hidden-empty { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 10px 0; font-size: 12px; }
  .reasoning-hidden-empty p { margin: 0; }

  .breakdown-head { flex-wrap: wrap; }
  .breakdown-sort { display: inline-flex; align-items: center; gap: 8px; font-size: 12px; }
  .breakdown-grid { display: grid; grid-template-columns: minmax(0, 1.45fr) minmax(0, 1fr); gap: 28px; align-items: start; }
  .breakdown-secondary-col { display: grid; gap: 22px; min-inline-size: 0; }
  .breakdown-panel { min-inline-size: 0; }
  .breakdown-panel-head { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; margin-block-end: 8px; }
  .breakdown-panel h3 { font-size: 13px; margin: 0; }
  .breakdown-meta { font-size: 11px; }
  .breakdown-table { inline-size: 100%; border-collapse: separate; border-spacing: 0; table-layout: auto; }
  .breakdown-table :is(th, td) { padding: 8px 0 8px 12px; border-block-start: 1px solid var(--line); border-block-end: 1px solid transparent; text-align: end; vertical-align: middle; }
  .breakdown-table thead th { border-block: 0; padding-block-start: 0; padding-block-end: 6px; font-size: 11px; color: var(--muted); font-weight: 550; white-space: nowrap; }
  .breakdown-table thead th.sorted-col { color: var(--text); }
  .breakdown-table tbody th { color: var(--text); }
  .breakdown-table tbody tr.rank-other th { color: var(--muted); }
  .breakdown-table tr :is(th, td):first-child { inline-size: 100%; max-inline-size: 0; padding-inline-start: 8px; border-inline-start: 1px solid transparent; text-align: start; font-weight: 550; }
  .breakdown-table tr :is(th, td):last-child { padding-inline-end: 8px; border-inline-end: 1px solid transparent; }
  .breakdown-table tbody tr:last-child :is(th, td) { border-block-end-color: var(--line); }
  .breakdown-table tbody tr.is-interactive { cursor: pointer; }
  .breakdown-table tbody tr.is-interactive:is(:hover, .is-active) > :first-child { border-start-start-radius: 6px; border-end-start-radius: 6px; }
  .breakdown-table tbody tr.is-interactive:is(:hover, .is-active) > :last-child { border-start-end-radius: 6px; border-end-end-radius: 6px; }
  .breakdown-table tbody tr.is-interactive:hover :is(th, td) {
    background: var(--hover);
    border-block-start-color: transparent;
    border-block-end-color: transparent;
  }
  .breakdown-table tbody tr.is-interactive:hover + tr :is(th, td) { border-block-start-color: transparent; }
  .breakdown-table tbody tr.is-active :is(th, td),
  .breakdown-table tbody tr.is-active:hover :is(th, td) {
    background: var(--nav-current);
    border-block-start-color: var(--selected-border);
    border-block-end-color: var(--selected-border);
  }
  .breakdown-table tbody tr.is-active > :first-child { border-inline-start-color: var(--selected-border); }
  .breakdown-table tbody tr.is-active > :last-child { border-inline-end-color: var(--selected-border); }
  .breakdown-table tbody tr.is-active:hover :is(th, td) { background: var(--option-highlighted); }
  .breakdown-table tbody tr.is-active + tr :is(th, td) { border-block-start-color: transparent; }
  .breakdown-table tbody tr.is-active th { color: var(--nav-current-text); }
  .breakdown-table tbody tr.is-active .rank-bar-track { background: color-mix(in srgb, var(--selected-border) 28%, transparent); }
  .breakdown-table td { font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; line-height: 1.3; }
  .breakdown-table td strong { display: block; font-weight: 560; }
  .breakdown-table td span { display: block; font-size: 11px; color: var(--muted); }
  .priced-count { white-space: nowrap; }
  .rank-name-row { display: flex; align-items: center; min-inline-size: 0; }
  .rank-plain-name { font-size: 12px; font-weight: 550; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .rank-plain-name.is-model { font-family: var(--mono); font-size: 11.5px; }
  .rank-bar-track { block-size: 4px; border-radius: 2px; background: var(--surface-raised); margin-block-start: 5px; overflow: hidden; }
  .rank-bar-fill { display: block; block-size: 100%; border-radius: 2px; background: var(--breakdown-model-bar); }
  .rank-bar-fill.client-bar { background: var(--breakdown-client-bar); }
  .rank-bar-fill.provider-bar { background: var(--breakdown-provider-bar); }
  .rank-bar-fill.other-bar { background: var(--breakdown-other-bar); }
  .rank-sub { margin-block-start: 4px; font-size: 11px; color: var(--muted); font-weight: 450; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
  .breakdown-panel :global(.breakdown-filter) {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    text-align: start;
    font: inherit;
    font-weight: 560;
    font-size: 12px;
    color: inherit;
    background: transparent;
    border: 0;
    padding: 0;
    margin: 0;
    border-radius: 4px;
    cursor: pointer;
    max-inline-size: 100%;
    min-inline-size: 0;
  }
  .breakdown-panel :global(.breakdown-filter.is-model) { font-family: var(--mono); font-size: 11.5px; }
  .breakdown-filter-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .breakdown-panel :global(.breakdown-active-check) { flex: none; color: var(--accent); }
  .breakdown-footer { margin-block-start: 8px; }
  .breakdown-note { margin: 7px 0 0; font-size: 12px; }

  @media (pointer: coarse) {
    .breakdown-table tbody tr.is-interactive th, .reasoning-panel :global(.hbar-row) { min-block-size: var(--touch-target); }
  }
  @media (forced-colors: active) {
    .timeline, .metrics, .reasoning-panel {
      --input: CanvasText;
      --cache: repeating-linear-gradient(135deg, CanvasText 0 1px, Canvas 1px 3px);
      --cache-write: repeating-linear-gradient(0deg, CanvasText 0 1px, Canvas 1px 3px);
      --output: repeating-linear-gradient(90deg, CanvasText 0 1px, Canvas 1px 3px);
    }
    .legend i[data-series], .hatched-fill, .kpi-bar span, .kpi-legend i, .hbar-fill, .rank-bar-fill { forced-color-adjust: none; }
    .hatched-fill { background: repeating-linear-gradient(-45deg, CanvasText 0 2px, Canvas 2px 4px); border: 1px solid CanvasText; }
    .reasoning-panel :global(.hide-unreported-toggle[data-pressed]) {
      forced-color-adjust: none;
      background: Highlight;
      border-color: Highlight;
      color: HighlightText;
    }
    .breakdown-table tbody tr.is-active :is(th, td),
    .reasoning-panel :global(.hbar-row[aria-pressed="true"]) { border-color: Highlight; }
    .reasoning-panel :global(.hbar-row[aria-pressed="true"]) { outline: 1px solid Highlight; }
  }
  @media (max-width: 1080px) {
    .analytics-split { grid-template-columns: minmax(0, 1fr); gap: 20px; }
    .reasoning-panel { border-inline-start: 0; border-block-start: 1px solid var(--line); padding-inline-start: 0; padding-block-start: 18px; }
  }
  @media (max-width: 980px) {
    .metrics { grid-template-columns: 1fr; gap: 14px; }
    .metric { padding-inline: 0; }
    .metric + .metric { border-inline-start: 0; border-block-start: 1px solid var(--line); padding-inline-start: 0; padding-block-start: 14px; }
    .breakdown-grid { grid-template-columns: minmax(0, 1fr); gap: 24px; }
  }
  @media (max-width: 720px) {
    .filters :global(button) { min-block-size: 44px; }
    .chart-selection :global(.icon-button) { inline-size: 44px; block-size: 44px; }
    .loading-metrics { grid-template-columns: minmax(0, 1fr); gap: 8px; }
    .loading-chart { min-block-size: 240px; }
  }
  @media (max-width: 480px) {
    .reasoning-panel :global(.hbar-row) {
      grid-template-columns: minmax(0, 1fr) auto;
      row-gap: 5px;
    }
    .hbar-track { grid-column: 1 / -1; grid-row: 2; }
    .hbar-nums { white-space: normal; }
    .breakdown-table :is(th, td) { padding-inline-start: 8px; }
  }
</style>
