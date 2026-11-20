<script lang="ts">
  import type { UsageBucket } from './api';
  import { axisMoney, compactCount, count, formatDay, formatDateTime, formatTime, guardEnter, money, tokens, uncachedInput } from './format';

  interface Props {
    buckets: UsageBucket[];
    kind: 'cost' | 'tokens';
    range: string;
    selection: string;
    onSelect: (start: string) => void;
  }
  let { buckets, kind, range, selection, onSelect }: Props = $props();
  let chartWidth = $state(0);
  let focusedBucket = $state('');
  let firstBucket = $derived(buckets[0]?.start ?? '');
  let availableFocus = $derived(buckets.some((bucket) => bucket.start === focusedBucket) ? focusedBucket : firstBucket);
  let maximum = $derived(axisMaximum(Math.max(0, ...buckets.map((bucket) =>
    kind === 'cost' ? Math.max(bucket.apiCost, bucket.codexValue) : tokens(bucket)
  ))));
  const maxLabels = $derived(Math.max(1, Math.min(6, Math.floor(chartWidth / 64))));
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

  function describe(bucket: UsageBucket) {
    const values = kind === 'cost'
      ? 'API cost ' + money(bucket.apiCost) + ', Codex value ' + money(bucket.codexValue)
      : 'Uncached input ' + count(uncachedInput(bucket)) + ', cached input ' + count(bucket.cachedInputTokens) + ', output ' + count(bucket.outputTokens);
    return formatDateTime(bucket.start) + ' · ' + values;
  }
</script>

<div class="chart-body">
  <div class="chart-axis" aria-hidden="true">
    {#each levels as level (level)}
      <span>{kind === 'cost' ? axisMoney(maximum * level) : compactCount(maximum * level)}</span>
    {/each}
  </div>

  <div class="chart-content" bind:clientWidth={chartWidth}>
    <div class="chart-grid" aria-hidden="true">
      {#each levels as level (level)}
        <span></span>
      {/each}
    </div>
    <div class="chart-columns" style:--columns={Math.max(buckets.length, 1)}>
      {#each buckets as bucket, index (bucket.start)}
        <button
          type="button"
          class="chart-column"
          class:stacked={kind === 'tokens'}
          aria-label={describe(bucket)}
          aria-pressed={selection === bucket.start}
          tabindex={availableFocus === bucket.start ? 0 : -1}
          onfocus={() => focusedBucket = bucket.start}
          onkeydown={moveFocus}
          onclick={() => onSelect(bucket.start)}
        >
          <span class="bars" aria-hidden="true">
            {#if kind === 'cost'}
              <span class="bar api-color" style:height={height(bucket.apiCost)}></span>
              <span class="bar codex-color" style:height={height(bucket.codexValue)}></span>
            {:else}
              <span class="bar input-color" style:height={height(uncachedInput(bucket))}></span>
              <span class="bar cache-color" style:height={height(bucket.cachedInputTokens)}></span>
              <span class="bar output-color" style:height={height(bucket.outputTokens)}></span>
            {/if}
          </span>
          <span aria-hidden="true" class="chart-tooltip" class:near-start={index < 2} class:near-end={index > buckets.length - 3}>
            <strong>{formatDateTime(bucket.start)}</strong>
            {#if kind === 'cost'}
              <span>API <b>{money(bucket.apiCost)}</b></span>
              <span>Codex <b>{money(bucket.codexValue)}</b></span>
            {:else}
              <span>Uncached input <b>{count(uncachedInput(bucket))}</b></span>
              <span>Cached input <b>{count(bucket.cachedInputTokens)}</b></span>
              <span>Output <b>{count(bucket.outputTokens)}</b></span>
            {/if}
          </span>
        </button>
      {/each}
    </div>
    <div class="chart-labels" style:--columns={Math.max(buckets.length, 1)} aria-hidden="true">
      {#each buckets as bucket, index (bucket.start)}
        <span>{index % stride === 0 ? (range === '24h' ? formatTime(bucket.start) : formatDay(bucket.start)) : ''}</span>
      {/each}
    </div>
  </div>
</div>

<style>
  .chart-body { display: grid; grid-template-columns: 47px minmax(0, 1fr); gap: 12px; block-size: 190px; }
  .chart-axis { display: flex; flex-direction: column; justify-content: space-between; align-items: flex-end; font-size: 10px; color: var(--muted); font-variant-numeric: tabular-nums; padding: 0 0 16px; transform: translateY(-5px); }
  .chart-content { position: relative; min-inline-size: 0; }
  .chart-grid { position: absolute; inset: 0 0 18px; display: flex; flex-direction: column; justify-content: space-between; pointer-events: none; }
  .chart-grid span { border-block-start: 1px solid var(--line); }
  .chart-columns, .chart-labels { display: grid; grid-template-columns: repeat(var(--columns), minmax(0, 1fr)); }
  .chart-columns { position: absolute; inset: 0 0 18px; }
  .chart-labels { position: absolute; inset: auto 0 0; color: var(--muted); font-size: 10px; white-space: nowrap; }
  .chart-labels span { text-align: center; }
  .chart-column { position: relative; appearance: none; background: transparent; border: 0; min-width: 0; padding: 0 2px; cursor: pointer; border-radius: 3px 3px 0 0; }
  .chart-column:hover,.chart-column[aria-pressed=true] { background: #ffffff08; }
  .chart-column[aria-pressed=true] { box-shadow: inset 0 -2px var(--accent); }
  .bars { block-size: 100%; display: flex; align-items: end; justify-content: center; gap: 2px; pointer-events: none; }
  .bar { display: inline-block; inline-size: 48%; max-inline-size: 24px; border-radius: 2px 2px 0 0; }
  .stacked .bars { flex-direction: column-reverse; justify-content: flex-start; align-items: center; gap: 0; }
  .stacked .bar { inline-size: 74%; max-inline-size: 44px; border-radius: 0; }
  .chart-tooltip { display: none; min-inline-size: 150px; white-space: nowrap; position: absolute; z-index: 2; inset: 7px auto auto 50%; transform: translateX(-50%); text-align: start; padding: 9px 10px; border: 1px solid var(--strong-line); background: var(--surface-raised); border-radius: 7px; box-shadow: 0 8px 26px #0007; color: var(--muted); font-size: 11px; pointer-events: none; }
  .chart-tooltip strong { color: var(--text); font-size: 11px; font-weight: 600; }
  .chart-tooltip span { display: flex; justify-content: space-between; gap: 14px; }
  .chart-tooltip b { font-weight: 500; color: var(--text); }
  .chart-column:is(:hover, :focus-visible) .chart-tooltip { display: grid; gap: 3px; }
  .chart-tooltip.near-start { transform: none; inset-inline-start: 0; }
  .chart-tooltip.near-end { transform: none; inset-inline-start: auto; inset-inline-end: 0; }
  @media (max-width: 600px) {
    .chart-body { block-size: 170px; grid-template-columns: 40px minmax(0, 1fr); gap: 6px; }
    .chart-column { padding-inline: 1px; }
    .chart-labels { font-size: 9px; }
  }
</style>
