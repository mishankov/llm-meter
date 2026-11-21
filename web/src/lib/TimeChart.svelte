<script module lang="ts">
  import type { GroupedUsageBucket, UsageBucket, UsageGroup } from './api';
  import { uncachedInput } from './format';

  export const tokenSeries = [
    { label: 'Uncached input', key: 'input', value: uncachedInput },
    { label: 'Cached input', key: 'cache', value: (bucket: UsageBucket) => bucket.cachedInputTokens },
    { label: 'Cache writes', key: 'cache-write', value: (bucket: UsageBucket) => bucket.cacheWriteTokens ?? 0 },
    { label: 'Output', key: 'output', value: (bucket: UsageBucket) => bucket.outputTokens }
  ];

  export type ChartSeries = { label: string; key: string; color: string; byStart: Map<string, number> };

  const colors = ['--usage-chart-1', '--usage-chart-2', '--usage-chart-3', '--usage-chart-4', '--usage-chart-5', '--usage-chart-6'];

  export function groupedSeries(
    entries: GroupedUsageBucket[], groups: UsageGroup[], unknown: string, metric: 'tokens' | 'estimatedCost'
  ): ChartSeries[] {
    const ranked = groups.filter((group) => group.tokens > 0 || group.estimatedCost > 0)
      .sort((a, b) => b.estimatedCost - a.estimatedCost || b.tokens - a.tokens || a.id.localeCompare(b.id));
    const top = ranked.slice(0, ranked.length > colors.length ? colors.length - 1 : colors.length);
    const series = top.map(({ id, name }, index) => ({
      label: name || id || unknown, key: 'group:' + id, color: colors[index], byStart: new Map<string, number>()
    }));
    const byID = new Map(top.map(({ id }, index) => [id, series[index]]));
    const other = { label: 'Other', key: 'other', color: colors[colors.length - 1], byStart: new Map<string, number>() };
    for (const entry of entries) {
      if (entry[metric] <= 0) continue;
      const group = byID.get(entry.id) ?? other;
      group.byStart.set(entry.start, (group.byStart.get(entry.start) ?? 0) + entry[metric]);
    }
    return [...series, other].filter((group) => group.byStart.size > 0);
  }
</script>

<script lang="ts">
  import { createTooltipController, Tooltip, TooltipContent, TooltipTrigger, type TooltipContentState } from '@ui-sdk/svelte/tooltip';
  import { axisMoneyFormatter, compactCount, count, formatDateTime, formatTime, formatUTCDay, guardEnter, money, partialPricing, tokens, unpricedBucket } from './format';

  interface Props {
    buckets: UsageBucket[];
    kind: 'cost' | 'tokens';
    range: string;
    selection: string;
    series?: ChartSeries[];
    onSelect: (start: string) => void;
  }
  let { buckets, kind, range, selection, series, onSelect }: Props = $props();
  const tooltip = createTooltipController<string>();
  let viewportWidth = $state(0);
  let plotWidth = $state(0);
  let focusedBucket = $state('');
  let firstBucket = $derived(buckets[0]?.start ?? '');
  let availableFocus = $derived(buckets.some((bucket) => bucket.start === focusedBucket) ? focusedBucket : firstBucket);
  let maximum = $derived(axisMaximum(Math.max(0, ...buckets.map((bucket) =>
    kind === 'cost' ? bucket.estimatedCost : tokens(bucket)
  ))));
  const formatTick = $derived(kind === 'cost' ? axisMoneyFormatter(maximum / 4) : compactCount);
  const labelsThatFit = $derived(Math.max(1, Math.floor(plotWidth / 64)));
  const maxLabels = $derived(plotWidth > viewportWidth ? labelsThatFit : Math.min(6, labelsThatFit));
  const stride = $derived(Math.max(1, Math.ceil(buckets.length / maxLabels)));
  const levels = [1, 0.75, 0.5, 0.25, 0];

  function axisMaximum(value: number) {
    if (!value) return kind === 'cost' ? 1 : 1000;
    const base = 10 ** Math.floor(Math.log10(value / 4));
    const step = [1, 2, 2.5, 5, 10].map((scale) => scale * base).find((step) => 4 * step >= value);
    return 4 * (step ?? base);
  }

  function height(value: number) {
    return Math.min(100, 100 * value / maximum) + '%';
  }

  function moveFocus(event: KeyboardEvent) {
    guardEnter(event);
    if (event.isComposing || event.keyCode === 229 || event.altKey || event.metaKey || event.ctrlKey) return;
    const button = event.currentTarget as HTMLButtonElement;
    const columns = Array.from(button.parentElement?.querySelectorAll('button') ?? []);
    const current = columns.indexOf(button);
    const next = event.key === 'ArrowLeft' ? current - 1 :
      event.key === 'ArrowRight' ? current + 1 :
      event.key === 'Home' ? 0 :
      event.key === 'End' ? columns.length - 1 : -1;
    if (next >= 0 && next < columns.length) {
      event.preventDefault();
      columns[next].focus();
    }
  }

  function bucketAmount(bucket: UsageBucket) {
    return unpricedBucket(bucket) ? 'Unpriced' : money(bucket.estimatedCost);
  }

  function bucketTime(bucket: UsageBucket) {
    return range === '24h' ? formatDateTime(bucket.start) : formatUTCDay(bucket.start) + ' UTC';
  }

  function describe(bucket: UsageBucket, coverage: string) {
    const values = kind === 'cost'
      ? 'Usage value ' + bucketAmount(bucket)
      : series ? 'Tokens ' + count(tokens(bucket)) : tokenSeries.map((series, index) =>
        (index ? series.label.toLowerCase() : series.label) + ' ' + count(series.value(bucket))
      ).join(', ');
    const breakdown = series?.flatMap((group) => {
      const value = group.byStart.get(bucket.start);
      return value ? [group.label + ' ' + formatAmount(value)] : [];
    }).join(', ');
    return [bucketTime(bucket), values, coverage, breakdown].filter(Boolean).join(' · ');
  }

  const formatAmount = (value: number) => kind === 'cost' ? money(value) : count(value);
</script>

<div class="chart-body">
  <div class="chart-axis" aria-hidden="true">
    {#each levels as level (level)}
      <span>{formatTick(maximum * level)}</span>
    {/each}
  </div>
  {#if range !== '24h'}<span class="axis-zone" aria-hidden="true">UTC</span>{/if}

  <div class="chart-content" bind:clientWidth={viewportWidth}>
    <div class="chart-plot" style:--columns={Math.max(buckets.length, 1)} bind:clientWidth={plotWidth}>
      <div class="chart-grid" aria-hidden="true">
        {#each levels as level (level)}
          <span></span>
        {/each}
      </div>
      <div class="chart-columns">
        {#each buckets as bucket (bucket.start)}
          {@const coverage = kind === 'cost' ? partialPricing(bucket) : ''}
          <TooltipTrigger
            controller={tooltip}
            payload={bucket.start}
            type="button"
            class="chart-column"
            aria-label={describe(bucket, coverage)}
            aria-pressed={selection === bucket.start}
            tabindex={availableFocus === bucket.start ? 0 : -1}
            onfocus={() => focusedBucket = bucket.start}
            onkeydown={moveFocus}
            onclick={() => onSelect(bucket.start)}
          >
            <!-- The bucket's accessible name already contains the full tooltip. -->
            {#snippet render({ props, state, children })}
              <button {...props} aria-labelledby={undefined}>
                {@render children?.(state)}
              </button>
            {/snippet}
            <span class="bars" class:stacked={kind === 'tokens' || Boolean(series)} aria-hidden="true">
              {#if series && !(kind === 'cost' && unpricedBucket(bucket))}
                {#each series as group (group.key)}
                  {@const amount = group.byStart.get(bucket.start) ?? 0}
                  {#if amount > 0}<span class="bar group-bar" style:background={'var(' + group.color + ')'} style:height={height(amount)}></span>{/if}
                {/each}
              {:else if kind === 'cost'}
                <span class="bar usage-color" class:unpriced-bar={unpricedBucket(bucket)} style:height={height(bucket.estimatedCost)}></span>
              {:else}
                {#each tokenSeries as series (series.key)}
                  <span class={'bar ' + series.key + '-color'} data-series={series.key} style:height={height(series.value(bucket))}></span>
                {/each}
              {/if}
            </span>
          </TooltipTrigger>
        {/each}
      </div>
      <div class="chart-labels" aria-hidden="true">
        {#each buckets as bucket, index (bucket.start)}
          <span>{index % stride === 0 ? (range === '24h' ? formatTime(bucket.start) : formatUTCDay(bucket.start)) : ''}</span>
        {/each}
      </div>
    </div>
  </div>
</div>
{#if plotWidth > viewportWidth}
  <p class="chart-scroll-hint muted">Scroll the chart to see more time windows.</p>
{/if}

<Tooltip controller={tooltip} type="label" delay={200} closeDelay={140}>
  <TooltipContent class="chart-tooltip" aria-hidden="true">
    {#snippet children({ payload }: TooltipContentState<string>)}
      {@const bucket = buckets.find((item) => item.start === payload)}
      {#if bucket}
        <strong>{bucketTime(bucket)}</strong>
        {#if kind === 'cost'}
          {@const coverage = partialPricing(bucket)}
          <span>Usage value <b>{bucketAmount(bucket)}</b></span>
          {#if coverage}<span>{coverage}</span>{/if}
        {:else if series}
          <span>Tokens <b>{count(tokens(bucket))}</b></span>
        {:else}
          {#each tokenSeries as series (series.key)}
            <span>{series.label} <b>{count(series.value(bucket))}</b></span>
          {/each}
        {/if}
        {#each series ?? [] as group (group.key)}
          {@const value = group.byStart.get(bucket.start)}
          {#if value}<span><span class="tooltip-group-name">{group.label}</span> <b>{formatAmount(value)}</b></span>{/if}
        {/each}
      {/if}
    {/snippet}
  </TooltipContent>
</Tooltip>

<style>
  .chart-body { position: relative; display: grid; grid-template-columns: minmax(47px, max-content) minmax(0, 1fr); gap: 12px; block-size: 190px; }
  .axis-zone { position: absolute; inset-inline-start: 0; inset-block-end: 0; color: var(--muted); font-size: 11px; }
  .chart-axis { display: flex; flex-direction: column; justify-content: space-between; align-items: flex-end; font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; padding: 0 0 16px; transform: translateY(-5px); }
  .chart-content { min-inline-size: 0; }
  .chart-plot { position: relative; block-size: 100%; }
  .chart-grid { position: absolute; inset: 0 0 18px; display: flex; flex-direction: column; justify-content: space-between; pointer-events: none; }
  .chart-grid span { border-block-start: 1px solid var(--line); }
  .chart-columns, .chart-labels { display: grid; grid-template-columns: repeat(var(--columns), minmax(0, 1fr)); }
  .chart-columns { position: absolute; inset: 0 0 18px; }
  .chart-labels { position: absolute; inset: auto 0 0; color: var(--muted); font-size: 11px; white-space: nowrap; }
  .chart-labels span { text-align: center; }
  :global(.chart-column) { position: relative; appearance: none; background: transparent; border: 0; min-width: 0; padding: 0 2px; cursor: pointer; border-radius: 3px 3px 0 0; }
  :global(.chart-column:hover), :global(.chart-column[aria-pressed=true]) { background: var(--chart-hover); }
  :global(.chart-column[aria-pressed=true]) { box-shadow: inset 0 0 0 1px var(--accent); }
  :global(.chart-column:focus-visible) { z-index: 1; outline-offset: -2px; }
  .bars { block-size: 100%; display: flex; align-items: end; justify-content: center; pointer-events: none; }
  .bar { display: inline-block; inline-size: 74%; max-inline-size: 44px; border-radius: 2px 2px 0 0; }
  .unpriced-bar { border-block-end: 2px dotted var(--muted); }
  .bars.stacked { flex-direction: column-reverse; justify-content: flex-start; align-items: center; }
  .stacked .bar { border-radius: 0; }
  :global(.chart-tooltip) { min-inline-size: 150px; max-inline-size: min(260px, 90vw); text-align: start; padding: 9px 10px; background: var(--surface-raised); border: 1px solid var(--strong-line); border-radius: 7px; color: var(--muted); font-size: 11px; }
  :global(.chart-tooltip[data-open]) { display: grid; gap: 3px; }
  :global(.chart-tooltip strong) { color: var(--text); font-weight: 600; }
  :global(.chart-tooltip span) { display: flex; justify-content: space-between; gap: 14px; }
  .tooltip-group-name { display: block; min-inline-size: 0; overflow-wrap: anywhere; }
  :global(.chart-tooltip b) { font-weight: 500; color: var(--text); flex: none; }
  .chart-scroll-hint { margin: -3px 0 0; font-size: 12px; }
  @media (max-width: 670px) {
    .chart-body { block-size: 170px; grid-template-columns: minmax(40px, max-content) minmax(0, 1fr); gap: 6px; }
    :global(.chart-column) { padding-inline: 1px; }
  }
  @media (pointer: coarse) {
    .chart-content { overflow-x: auto; }
    .chart-plot { min-inline-size: calc(var(--columns) * var(--touch-target)); }
    .chart-labels span:first-child { text-align: start; }
    .chart-labels span:last-child { text-align: end; }
  }
  @media (forced-colors: active) {
    .bar { forced-color-adjust: none; }
    .bar.usage-color { background: CanvasText; }
    .bar.unpriced-bar { background: transparent; border-color: CanvasText; }
    :global(.chart-column[aria-pressed=true])::after { content: ''; position: absolute; inset: 0; border: 1px solid Highlight; pointer-events: none; }
    :global(.chart-column:focus-visible) { outline-color: CanvasText; }
  }
</style>
