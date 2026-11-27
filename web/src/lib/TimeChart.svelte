<script module lang="ts">
  import type { UsageBucket } from './api';
  import { uncachedInput } from './format';

  export const tokenSeries = [
    { label: 'Uncached input', key: 'input', value: uncachedInput },
    { label: 'Cached input', key: 'cache', value: (bucket: UsageBucket) => bucket.cachedInputTokens },
    { label: 'Cache writes', key: 'cache-write', value: (bucket: UsageBucket) => bucket.cacheWriteTokens ?? 0 },
    { label: 'Output', key: 'output', value: (bucket: UsageBucket) => bucket.outputTokens }
  ];
</script>

<script lang="ts">
  import { createTooltipController, Tooltip, TooltipContent, TooltipTrigger, type TooltipContentState } from '@ui-sdk/svelte/tooltip';
  import { axisMoneyFormatter, compactCount, count, formatDateTime, formatDateTimeRange, formatTime, formatUTCDay, guardEnter, money, partialPricing, tokens, unpricedBucket } from './format';

  interface Props {
    buckets: UsageBucket[];
    bucketSeconds?: number;
    rangeStart?: string;
    rangeEnd?: string;
    kind: 'cost' | 'tokens';
    range: string;
    selection: string;
    stackedTokens?: boolean;
    onSelect: (start: string) => void;
    onSelectRange?: (from: string, to: string) => void;
  }
  let { buckets, bucketSeconds, rangeStart, rangeEnd, kind, range, selection, stackedTokens = true, onSelect, onSelectRange }: Props = $props();
  const tooltip = createTooltipController<string>();
  let columnsEl = $state<HTMLDivElement>();
  let viewportWidth = $state(0);
  let plotWidth = $state(0);
  let focusedBucket = $state('');
  let activePayload = $state<string | null>(null);

  let dragAnchorStart = $state<string | null>(null);
  let dragCurrentStart = $state<string | null>(null);
  let dragging = $state(false);
  let touchDragging = $state(false);
  let suppressNextClick = false;
  let lastAnchorStart = $state<string | null>(null);
  let kbAnchorStart = $state<string | null>(null);
  let kbCurrentStart = $state<string | null>(null);
  let rangeAnnouncement = $state('');

  let touchHoldTimer: ReturnType<typeof setTimeout> | null = null;
  let touchPointerId: number | null = null;
  let touchStartX = 0;
  let touchStartY = 0;
  const TOUCH_HOLD_MS = 320;
  const TOUCH_MOVE_TOLERANCE = 8;

  let bucketDurationMs = $derived.by(() => {
    if (bucketSeconds && bucketSeconds > 0) return bucketSeconds * 1000;
    if (buckets.length >= 2) {
      const diff = Date.parse(buckets[1].start) - Date.parse(buckets[0].start);
      if (Number.isFinite(diff) && diff >= 60_000) return diff;
    }
    if (range === '1h') return 5 * 60_000;
    if (range === '24h') return 3_600_000;
    return 86_400_000;
  });
  let isSubDay = $derived(bucketDurationMs < 86_400_000);
  let denseTimeline = $derived(buckets.length > 48);
  let bucketStartsKey = $derived(buckets.map((bucket) => bucket.start).join('|'));
  let spansMultipleDays = $derived.by(() => {
    if (buckets.length < 2) return false;
    const first = new Date(buckets[0].start);
    const last = new Date(buckets[buckets.length - 1].start);
    return first.toDateString() !== last.toDateString();
  });
  let firstBucket = $derived(buckets[0]?.start ?? '');
  let availableFocus = $derived(buckets.some((bucket) => bucket.start === focusedBucket) ? focusedBucket : firstBucket);
  let maximum = $derived(axisMaximum(Math.max(0, ...buckets.map((bucket) =>
    kind === 'cost' ? bucket.estimatedCost : tokens(bucket)
  ))));
  const formatTick = $derived(kind === 'cost' ? axisMoneyFormatter(maximum / 4) : compactCount);
  const labelsThatFit = $derived(Math.max(1, Math.floor(plotWidth / 64)));
  const maxLabels = $derived(plotWidth > viewportWidth ? labelsThatFit : Math.min(6, labelsThatFit));
  const stride = $derived(Math.max(1, Math.ceil(buckets.length / maxLabels)));
  const activeIndex = $derived(activePayload ? buckets.findIndex((item) => item.start === activePayload) : -1);
  const dragAnchor = $derived(dragAnchorStart ? buckets.findIndex((item) => item.start === dragAnchorStart) : -1);
  const dragCurrent = $derived(dragCurrentStart ? buckets.findIndex((item) => item.start === dragCurrentStart) : -1);
  const kbAnchor = $derived(kbAnchorStart ? buckets.findIndex((item) => item.start === kbAnchorStart) : -1);
  const kbCurrent = $derived(kbCurrentStart ? buckets.findIndex((item) => item.start === kbCurrentStart) : -1);
  // Keep tooltips inside the chart area on the opposite horizontal side of the hovered column,
  // or below on narrow viewports, so they never cover the header controls or legend above.
  const tooltipSide = $derived<'left' | 'right' | 'bottom'>(
    viewportWidth > 0 && viewportWidth < 460
      ? 'bottom'
      : activeIndex >= Math.floor(buckets.length / 2)
        ? 'left'
        : 'right'
  );
  const activeRange = $derived.by(() => {
    if (dragging && dragAnchor >= 0 && dragCurrent >= 0) {
      return { start: Math.min(dragAnchor, dragCurrent), end: Math.max(dragAnchor, dragCurrent), keyboard: false };
    }
    if (kbAnchor >= 0 && kbCurrent >= 0 && kbAnchor !== kbCurrent) {
      return { start: Math.min(kbAnchor, kbCurrent), end: Math.max(kbAnchor, kbCurrent), keyboard: true };
    }
    return null;
  });
  const activeRangeSummary = $derived.by(() => {
    if (!activeRange) return '';
    const bounds = clampedRangeBounds(activeRange.start, activeRange.end);
    if (!bounds) return '';
    const fromBucket = buckets[activeRange.start];
    const toBucket = buckets[activeRange.end];
    const clipped = bounds.fromISO !== new Date(Date.parse(fromBucket.start)).toISOString()
      || bounds.toISO !== new Date(Date.parse(toBucket.start) + bucketDurationMs).toISOString();
    const label = isSubDay || clipped
      ? formatDateTimeRange(bounds.fromISO, bounds.toISO)
      : `${formatUTCDay(fromBucket.start)} – ${formatUTCDay(toBucket.start)} UTC`;
    const windows = activeRange.end - activeRange.start + 1;
    return `${label} · ${windows} windows`;
  });
  const levels = [1, 0.75, 0.5, 0.25, 0];

  $effect(() => {
    void kind;
    void stackedTokens;
    tooltip.close();
  });

  $effect(() => {
    void bucketStartsKey;
    const valid = new Set(buckets.map((bucket) => bucket.start));
    if (lastAnchorStart && !valid.has(lastAnchorStart)) lastAnchorStart = null;
    if ((kbAnchorStart && !valid.has(kbAnchorStart)) || (kbCurrentStart && !valid.has(kbCurrentStart))) {
      kbAnchorStart = null;
      kbCurrentStart = null;
    }
    if ((dragAnchorStart && !valid.has(dragAnchorStart)) || (dragCurrentStart && !valid.has(dragCurrentStart))) {
      cancelTouchHold();
      dragAnchorStart = null;
      dragCurrentStart = null;
      dragging = false;
      touchDragging = false;
    }
  });

  $effect(() => () => cancelTouchHold());

  $effect(() => {
    if (!touchDragging) return;
    const preventScroll = (event: TouchEvent) => {
      if (event.cancelable) event.preventDefault();
    };
    window.addEventListener('touchmove', preventScroll, { passive: false });
    return () => window.removeEventListener('touchmove', preventScroll);
  });

  function cancelTouchHold() {
    if (touchHoldTimer !== null) {
      clearTimeout(touchHoldTimer);
      touchHoldTimer = null;
    }
    touchPointerId = null;
  }

  function clearInteractionRange() {
    cancelTouchHold();
    dragAnchorStart = null;
    dragCurrentStart = null;
    dragging = false;
    touchDragging = false;
    kbAnchorStart = null;
    kbCurrentStart = null;
  }

  function axisMaximum(value: number) {
    if (!value) return kind === 'cost' ? 1 : 1000;
    const base = 10 ** Math.floor(Math.log10(value / 4));
    const step = [1, 2, 2.5, 5, 10].map((scale) => scale * base).find((step) => 4 * step >= value);
    return 4 * (step ?? base);
  }

  function height(value: number) {
    return Math.min(100, 100 * value / maximum) + '%';
  }

  function clampedRangeBounds(startIdx: number, endIdx: number) {
    const first = buckets[startIdx];
    const last = buckets[endIdx];
    if (!first || !last) return null;
    let startMs = Date.parse(first.start);
    let endMs = Date.parse(last.start) + bucketDurationMs;
    const minMs = rangeStart ? Date.parse(rangeStart) : NaN;
    const maxMs = rangeEnd ? Date.parse(rangeEnd) : NaN;
    if (Number.isFinite(minMs)) startMs = Math.max(startMs, minMs);
    if (Number.isFinite(maxMs)) endMs = Math.min(endMs, maxMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs) || endMs <= startMs) return null;
    return { fromISO: new Date(startMs).toISOString(), toISO: new Date(endMs).toISOString(), spanMs: endMs - startMs };
  }

  function commitIndexRange(a: number, b: number) {
    if (!onSelectRange || buckets.length < 2) return;
    const startIdx = Math.max(0, Math.min(a, b));
    const endIdx = Math.min(buckets.length - 1, Math.max(a, b));
    if (startIdx === endIdx) return;
    const bounds = clampedRangeBounds(startIdx, endIdx);
    if (!bounds || bounds.spanMs < 60_000) return;
    clearInteractionRange();
    tooltip.close();
    onSelectRange(bounds.fromISO, bounds.toISO);
  }

  function indexFromClientX(clientX: number) {
    if (!columnsEl || !buckets.length) return null;
    const rect = columnsEl.getBoundingClientRect();
    if (rect.width <= 0) return null;
    const ratio = (clientX - rect.left) / rect.width;
    return Math.max(0, Math.min(buckets.length - 1, Math.floor(ratio * buckets.length)));
  }

  function startDrag(index: number, event: PointerEvent) {
    if (!onSelectRange || buckets.length < 2) return;
    const start = buckets[index]?.start;
    if (!start) return;
    if (event.pointerType === 'touch') {
      cancelTouchHold();
      touchPointerId = event.pointerId;
      touchStartX = event.clientX;
      touchStartY = event.clientY;
      touchHoldTimer = setTimeout(() => {
        touchHoldTimer = null;
        kbAnchorStart = null;
        kbCurrentStart = null;
        dragAnchorStart = start;
        dragCurrentStart = start;
        dragging = true;
        touchDragging = true;
        tooltip.close();
        if (touchPointerId !== null) {
          try { columnsEl?.setPointerCapture?.(touchPointerId); } catch {}
        }
      }, TOUCH_HOLD_MS);
      return;
    }
    if (event.button !== 0) return;
    kbAnchorStart = null;
    kbCurrentStart = null;
    dragAnchorStart = start;
    dragCurrentStart = start;
    dragging = false;
  }

  function handleWindowPointerMove(event: PointerEvent) {
    if (touchHoldTimer !== null && event.pointerId === touchPointerId) {
      if (Math.hypot(event.clientX - touchStartX, event.clientY - touchStartY) > TOUCH_MOVE_TOLERANCE) {
        cancelTouchHold();
      }
      return;
    }
    if (dragAnchor < 0) return;
    if (touchDragging && touchPointerId !== null && event.pointerId !== touchPointerId) return;
    const idx = indexFromClientX(event.clientX);
    if (idx === null) return;
    dragCurrentStart = buckets[idx].start;
    if (idx !== dragAnchor && !dragging) {
      dragging = true;
      tooltip.close();
    }
  }

  function handleWindowPointerUp(event: PointerEvent) {
    if (touchHoldTimer !== null && event.pointerId === touchPointerId) {
      cancelTouchHold();
      return;
    }
    if (dragAnchor < 0) return;
    if (touchDragging && touchPointerId !== null && event.pointerId !== touchPointerId) return;
    const anchor = dragAnchor;
    const current = dragCurrent;
    const wasDragging = dragging;
    const wasTouch = touchDragging;
    if (touchPointerId !== null) {
      try { columnsEl?.releasePointerCapture?.(touchPointerId); } catch {}
    }
    cancelTouchHold();
    dragAnchorStart = null;
    dragCurrentStart = null;
    dragging = false;
    touchDragging = false;
    if (wasDragging && current >= 0 && current !== anchor) {
      suppressNextClick = true;
      queueMicrotask(() => { suppressNextClick = false; });
      commitIndexRange(anchor, current);
    } else if (wasTouch && wasDragging) {
      suppressNextClick = true;
      queueMicrotask(() => { suppressNextClick = false; });
    }
  }

  function handleColumnClick(index: number, bucket: UsageBucket, event: MouseEvent) {
    if (suppressNextClick) {
      suppressNextClick = false;
      return;
    }
    const anchorStart = lastAnchorStart ?? (selection || null);
    const anchorIdx = anchorStart ? buckets.findIndex((item) => item.start === anchorStart) : -1;
    if (event.shiftKey && onSelectRange && anchorIdx >= 0 && anchorIdx !== index) {
      lastAnchorStart = bucket.start;
      commitIndexRange(anchorIdx, index);
      return;
    }
    lastAnchorStart = bucket.start;
    kbAnchorStart = null;
    kbCurrentStart = null;
    onSelect(bucket.start);
  }

  function moveFocus(index: number, event: KeyboardEvent) {
    guardEnter(event);
    if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || event.altKey || event.metaKey || event.ctrlKey) return;
    if (event.key === 'Escape') {
      if (activeRange || dragAnchor >= 0) {
        event.preventDefault();
        clearInteractionRange();
      }
      return;
    }
    if ((event.key === 'Enter' || event.key === ' ') && kbAnchor >= 0 && kbCurrent >= 0 && kbAnchor !== kbCurrent) {
      event.preventDefault();
      if (event.repeat) return;
      commitIndexRange(kbAnchor, kbCurrent);
      return;
    }
    const button = event.currentTarget as HTMLButtonElement;
    const columns = Array.from(button.parentElement?.querySelectorAll('button') ?? []);
    const current = columns.indexOf(button);
    const next = event.key === 'ArrowLeft' ? current - 1 :
      event.key === 'ArrowRight' ? current + 1 :
      event.key === 'Home' ? 0 :
      event.key === 'End' ? columns.length - 1 : -1;
    if (next >= 0 && next < columns.length) {
      event.preventDefault();
      if (event.shiftKey && onSelectRange && buckets.length > 1) {
        if (kbAnchor < 0) kbAnchorStart = buckets[index].start;
        kbCurrentStart = buckets[next].start;
        tooltip.close();
        const anchorIdx = kbAnchor < 0 ? index : kbAnchor;
        const startIdx = Math.min(anchorIdx, next);
        const endIdx = Math.max(anchorIdx, next);
        const bounds = clampedRangeBounds(startIdx, endIdx);
        if (bounds) {
          rangeAnnouncement = `Selected ${endIdx - startIdx + 1} windows: ${formatDateTimeRange(bounds.fromISO, bounds.toISO)}. Press Enter to filter global time range.`;
        }
      } else {
        kbAnchorStart = null;
        kbCurrentStart = null;
      }
      columns[next].focus();
    }
  }

  function bucketAmount(bucket: UsageBucket) {
    return unpricedBucket(bucket) ? 'Unpriced' : money(bucket.estimatedCost);
  }

  function bucketTime(bucket: UsageBucket) {
    return isSubDay ? formatDateTime(bucket.start) : formatUTCDay(bucket.start) + ' UTC';
  }

  function axisLabel(bucket: UsageBucket) {
    if (!isSubDay) return formatUTCDay(bucket.start);
    return bucketDurationMs >= 6 * 3_600_000 && spansMultipleDays
      ? formatDateTime(bucket.start)
      : formatTime(bucket.start);
  }

  function describe(bucket: UsageBucket, coverage: string) {
    const values = kind === 'cost'
      ? 'Usage value ' + bucketAmount(bucket)
      : !stackedTokens ? 'Tokens ' + count(tokens(bucket)) : tokenSeries.map((series, index) =>
        (index ? series.label.toLowerCase() : series.label) + ' ' + count(series.value(bucket))
      ).join(', ');
    return [bucketTime(bucket), values, coverage].filter(Boolean).join(' · ');
  }
</script>

<svelte:window
  onpointermove={handleWindowPointerMove}
  onpointerup={handleWindowPointerUp}
  onpointercancel={clearInteractionRange}
  onblur={clearInteractionRange}
/>

<div class="chart-body" class:is-dragging={dragging}>
  <div class="chart-axis" aria-hidden="true">
    {#each levels as level (level)}
      <span>{formatTick(maximum * level)}</span>
    {/each}
  </div>
  {#if !isSubDay}<span class="axis-zone" aria-hidden="true">UTC</span>{/if}

  <div class="chart-viewport">
    {#if activeRange && activeRangeSummary}
      <div class="brush-pill" role="status">
        <span>{activeRangeSummary}</span>
        {#if activeRange.keyboard}<span class="muted">· Press Enter to apply</span>{/if}
      </div>
    {/if}
    <div class="chart-content" class:is-dense={denseTimeline} bind:clientWidth={viewportWidth}>
      <div class="chart-plot" class:is-dense={denseTimeline} style:--columns={Math.max(buckets.length, 1)} bind:clientWidth={plotWidth}>
        <div class="chart-grid" aria-hidden="true">
        {#each levels as level (level)}
          <span></span>
        {/each}
      </div>
      <div class="chart-columns" role="group" aria-label="Time buckets" bind:this={columnsEl}>
        {#each buckets as bucket, index (bucket.start)}
          {@const coverage = kind === 'cost' ? partialPricing(bucket) : ''}
          {@const bucketTokens = tokens(bucket)}
          {@const inRange = Boolean(activeRange && index >= activeRange.start && index <= activeRange.end)}
          <TooltipTrigger
            controller={tooltip}
            payload={bucket.start}
            type="button"
            class="chart-column"
            data-in-range={inRange || undefined}
            data-range-start={activeRange?.start === index || undefined}
            data-range-end={activeRange?.end === index || undefined}
            aria-label={describe(bucket, coverage)}
            aria-pressed={selection === bucket.start}
            tabindex={availableFocus === bucket.start ? 0 : -1}
            onfocus={() => { focusedBucket = bucket.start; activePayload = bucket.start; }}
            onpointerenter={() => { activePayload = bucket.start; }}
            onpointerdown={(event) => startDrag(index, event)}
            onkeydown={(event) => moveFocus(index, event)}
            onclick={(event) => handleColumnClick(index, bucket, event)}
          >
            <!-- The bucket's accessible name already contains the full tooltip. -->
            {#snippet render({ props, state, children })}
              <button {...props} aria-labelledby={undefined}>
                {@render children?.(state)}
              </button>
            {/snippet}
            <span class="bars" class:stacked={kind === 'tokens' && stackedTokens} aria-hidden="true">
              {#if kind === 'cost'}
                <span
                  class="bar usage-color"
                  class:unpriced-bar={unpricedBucket(bucket)}
                  class:nonzero={bucket.estimatedCost > 0}
                  style:height={height(bucket.estimatedCost)}
                ></span>
              {:else if !stackedTokens}
                <span
                  class="bar total-token-bar"
                  class:nonzero={bucketTokens > 0}
                  style:height={height(bucketTokens)}
                ></span>
              {:else}
                {#each tokenSeries as series (series.key)}
                  {@const amount = series.value(bucket)}
                  <span
                    class={'bar ' + series.key + '-color'}
                    class:nonzero={amount > 0}
                    data-series={series.key}
                    style:height={height(amount)}
                  ></span>
                {/each}
              {/if}
            </span>
          </TooltipTrigger>
        {/each}
      </div>
      <div class="chart-labels" aria-hidden="true">
        {#each buckets as bucket, index (bucket.start)}
          <span>{index % stride === 0 ? axisLabel(bucket) : ''}</span>
        {/each}
      </div>
      </div>
    </div>
  </div>
</div>
<span class="sr-only" aria-live="polite" aria-atomic="true">{rangeAnnouncement}</span>
{#if plotWidth > viewportWidth}
  <p class="chart-scroll-hint muted">
    Scroll the chart to see more time windows{#if onSelectRange && buckets.length > 1} · drag or press and hold to select a range{/if}.
  </p>
{:else if onSelectRange && buckets.length > 1}
  <p class="chart-brush-hint muted">Drag across bars, Shift+Click, or Shift+Arrow to set global time range · click one bar to filter requests</p>
{/if}

<Tooltip
  controller={tooltip}
  type="label"
  delay={160}
  closeDelay={120}
  disabled={dragging}
  onPayloadChange={(payload) => { activePayload = typeof payload === 'string' ? payload : null; }}
>
  <TooltipContent
    class="chart-tooltip"
    aria-hidden="true"
    side={tooltipSide}
    align={tooltipSide === 'bottom' ? 'center' : 'start'}
    sideOffset={8}
    alignOffset={tooltipSide === 'bottom' ? 0 : 6}
    collisionPadding={10}
  >
    {#snippet children({ payload }: TooltipContentState<string>)}
      {@const bucket = buckets.find((item) => item.start === payload)}
      {#if bucket}
        <strong>{bucketTime(bucket)}</strong>
        {#if kind === 'cost'}
          {@const coverage = partialPricing(bucket)}
          <span>Usage value <b>{bucketAmount(bucket)}</b></span>
          {#if coverage}<span>{coverage}</span>{/if}
        {:else if !stackedTokens}
          <span>Tokens <b>{count(tokens(bucket))}</b></span>
        {:else}
          {#each tokenSeries as series (series.key)}
            <span>{series.label} <b>{count(series.value(bucket))}</b></span>
          {/each}
        {/if}
      {/if}
    {/snippet}
  </TooltipContent>
</Tooltip>

<style>
  .chart-body { position: relative; display: grid; grid-template-columns: minmax(47px, max-content) minmax(0, 1fr); gap: 12px; block-size: 196px; }
  .chart-body.is-dragging { user-select: none; touch-action: none; cursor: ew-resize; }
  .axis-zone { position: absolute; inset-inline-start: 0; inset-block-end: 0; color: var(--muted); font-size: 10px; line-height: 1; }
  .chart-axis { display: flex; flex-direction: column; justify-content: space-between; align-items: flex-end; font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; padding: 0 0 18px; transform: translateY(-5px); }
  .chart-viewport { position: relative; min-inline-size: 0; block-size: 100%; }
  .chart-content { min-inline-size: 0; block-size: 100%; }
  .chart-content.is-dense { overflow-x: auto; }
  .chart-plot { position: relative; block-size: 100%; }
  .chart-plot.is-dense { min-inline-size: calc(var(--columns) * 12px); }
  .chart-plot.is-dense :global(.chart-column) { padding-inline: 1px; }
  .chart-plot.is-dense .chart-labels span:first-child { text-align: start; }
  .chart-plot.is-dense .chart-labels span:last-child { text-align: end; }
  .brush-pill {
    position: absolute;
    inset-block-start: 6px;
    inset-inline-start: 50%;
    transform: translateX(-50%);
    max-inline-size: calc(100% - 12px);
    overflow: hidden;
    text-overflow: ellipsis;
    z-index: 3;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 9px;
    border-radius: 5px;
    border: 1px solid var(--selected-border);
    background: var(--surface-raised);
    color: var(--text);
    font-size: 11px;
    font-weight: 560;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    box-shadow: 0 4px 14px var(--popover-shadow);
    pointer-events: none;
  }
  .chart-grid { position: absolute; inset: 0 0 18px; display: flex; flex-direction: column; justify-content: space-between; pointer-events: none; }
  .chart-grid span { border-block-start: 1px solid var(--line); }
  .chart-columns, .chart-labels { display: grid; grid-template-columns: repeat(var(--columns), minmax(0, 1fr)); }
  .chart-columns { position: absolute; inset: 0 0 18px; }
  .chart-labels { position: absolute; inset: auto 0 0; color: var(--muted); font-size: 11px; white-space: nowrap; }
  .chart-labels span { text-align: center; }
  :global(.chart-column) { position: relative; appearance: none; background: transparent; border: 0; min-width: 0; padding: 0 2px; cursor: pointer; border-radius: 3px 3px 0 0; }
  :global(.chart-column:hover), :global(.chart-column[aria-pressed=true]) { background: var(--chart-hover); }
  :global(.chart-column[aria-pressed=true]) { box-shadow: inset 0 0 0 1px var(--accent); }
  :global(.chart-column[data-in-range]) {
    background: var(--nav-current);
    border-radius: 0;
    box-shadow: inset 0 1px 0 var(--accent), inset 0 -1px 0 var(--accent);
  }
  :global(.chart-column[data-range-start]) {
    border-start-start-radius: 4px;
    box-shadow: inset 1px 1px 0 var(--accent), inset 0 -1px 0 var(--accent);
  }
  :global(.chart-column[data-range-end]) {
    border-start-end-radius: 4px;
    box-shadow: inset -1px 1px 0 var(--accent), inset 0 -1px 0 var(--accent);
  }
  :global(.chart-column:focus-visible) { z-index: 1; outline-offset: -2px; }
  .bars { block-size: 100%; display: flex; align-items: end; justify-content: center; pointer-events: none; }
  .bar { display: inline-block; inline-size: 74%; max-inline-size: 44px; border-radius: 2px 2px 0 0; }
  .bar.nonzero { min-block-size: 2px; }
  .total-token-bar { background: var(--breakdown-model-bar); }
  .unpriced-bar { border-block-end: 2px dotted var(--muted); }
  .bars.stacked { flex-direction: column-reverse; justify-content: flex-start; align-items: center; }
  .stacked .bar { border-radius: 0; }
  .stacked .bar.nonzero { min-block-size: 1px; }
  :global(.chart-tooltip) { pointer-events: none; min-inline-size: 156px; max-inline-size: min(260px, 90vw); text-align: start; padding: 9px 11px; background: var(--surface-raised); border: 1px solid var(--strong-line); border-radius: 7px; box-shadow: 0 8px 24px var(--popover-shadow); color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; }
  :global(.chart-tooltip[data-open]) { display: grid; gap: 3px; }
  :global(.chart-tooltip strong) { color: var(--text); font-weight: 600; }
  :global(.chart-tooltip span) { display: flex; justify-content: space-between; gap: 14px; }
  :global(.chart-tooltip b) { font-weight: 500; color: var(--text); flex: none; }
  .chart-scroll-hint, .chart-brush-hint { margin: -2px 0 0; font-size: 11px; }
  @media (max-width: 670px) {
    .chart-body { block-size: 174px; grid-template-columns: minmax(40px, max-content) minmax(0, 1fr); gap: 6px; }
    :global(.chart-column) { padding-inline: 1px; }
    .chart-brush-hint { display: none; }
  }
  @media (pointer: coarse) {
    .chart-content { overflow-x: auto; }
    .chart-plot, .chart-plot.is-dense { min-inline-size: calc(var(--columns) * var(--touch-target)); }
    .chart-labels span:first-child { text-align: start; }
    .chart-labels span:last-child { text-align: end; }
    .chart-brush-hint { display: none; }
  }
  @media (forced-colors: active) {
    .bar { forced-color-adjust: none; }
    .bar.usage-color, .bar.total-token-bar { background: CanvasText; }
    .bar.unpriced-bar { background: transparent; border-color: CanvasText; }
    :global(.chart-column[aria-pressed=true])::after,
    :global(.chart-column[data-in-range])::after { content: ''; position: absolute; inset: 0; border: 1px solid Highlight; pointer-events: none; }
    :global(.chart-column:focus-visible) { outline-color: CanvasText; }
  }
</style>
