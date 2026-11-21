<script lang="ts">
  import Clock3Icon from '@lucide/svelte/icons/clock-3';
  import SlidersHorizontalIcon from '@lucide/svelte/icons/sliders-horizontal';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { Meter, MeterIndicator, MeterTrack } from '@ui-sdk/svelte/meter';
  import { SegmentedControl, SegmentedControlItem } from '@ui-sdk/svelte/segmented-control';
  import type { Usage, UsageGroup } from './api';
  import { compactCount, count, formatDateTime, formatTime, guardEnter, money, tokens } from './format';
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
    refresh: () => void;
    onPrevious: () => void;
    onNext: () => void;
  }

  let { data, filterOptions, requestPage, loading, error, updatedAt, range, source, client, model, bucket, canPrevious, update, refresh, onPrevious, onNext }: Props = $props();
  let ranking = $state('models');
  let unit = $state('dollars');
  let groups = $derived(
    (data?.groups[ranking === 'models' ? 'models' : 'clients'] ?? [])
      .slice().sort((a, b) => groupValue(b) - groupValue(a) || a.name.localeCompare(b.name)).slice(0, 6)
  );
  let maxRank = $derived(Math.max(0, ...groups.map(groupValue)));

  const sourceOptions = [
    { value: '', label: 'All sources' },
    { value: 'api', label: 'OpenAI API' },
    { value: 'codex', label: 'Codex subscription' }
  ];

  function groupValue(group: UsageGroup) {
    return unit === 'tokens' ? group.tokens : group.apiCost + group.codexValue;
  }

  function selectGroup(group: UsageGroup) {
    if (ranking === 'models') update('model', model === group.id ? '' : group.id);
    else update('client', client === group.id ? '' : group.id);
  }
</script>

{#snippet costLegend()}
  <span><i class="api-color" aria-hidden="true"></i>API cost</span>
  <span><i class="codex-color" aria-hidden="true"></i>Codex value</span>
{/snippet}

<header class="page-head">
  <div>
    <h1>Usage overview</h1>
    {#if updatedAt}<p class="update-line"><span class="live-dot"></span> Updated {formatTime(updatedAt)}</p>{/if}
  </div>
  <SegmentedControl class="segmented" aria-label="Date range" value={range} onValueChange={(value) => { if (value) update('range', value); }}>
    <SegmentedControlItem value="24h">Last 24h</SegmentedControlItem>
    <SegmentedControlItem value="7d">7 days</SegmentedControlItem>
    <SegmentedControlItem value="30d">30 days</SegmentedControlItem>
  </SegmentedControl>
</header>

<div class="filters">
  <SlidersHorizontalIcon size={16} aria-hidden="true" />
  <Filter label="Source" value={source} items={sourceOptions} onChange={(value) => update('source', value)} />
  <Filter label="Client" value={client} items={[{ value: '', label: 'All clients' }, ...(filterOptions?.clients ?? []).map((choice) => ({ value: choice.id, label: choice.name }))]} onChange={(value) => update('client', value)} />
  <Filter label="Model" value={model} items={[{ value: '', label: 'All models' }, ...(filterOptions?.models ?? []).map((choice) => ({ value: choice, label: choice }))]} onChange={(value) => update('model', value)} />
</div>

{#if error}
  <div class="feedback error-feedback" role="alert">
    <span>{error}</span>
    <Button class="secondary-button" onclick={refresh} disabled={loading}>Retry</Button>
  </div>
{/if}

{#if !data}
  {#if !error}<div class="empty-state" role="status">Loading usage…</div>{/if}
{:else}
  <div class="metrics">
    <section class="metric" aria-label="Estimated API cost">
      <div class="metric-label">Estimated API cost</div>
      <strong>{money(data.summary.apiCost)}</strong>
      <span class="metric-caption">API-key requests</span>
    </section>
    <section class="metric" aria-label="Codex value at API rates">
      <div class="metric-label">Codex value at API rates</div>
      <strong>{money(data.summary.codexValue)}</strong>
      <span class="metric-caption">Subscription requests</span>
    </section>
    <section class="metric" aria-label="Total tokens">
      <div class="metric-label">Total tokens</div>
      <strong title={count(tokens(data.summary))}>{compactCount(tokens(data.summary))}</strong>
      <span class="metric-caption">{count(data.summary.requestCount)} {data.summary.requestCount === 1 ? 'request' : 'requests'}</span>
    </section>
  </div>

  <div class="chart-row">
    <section class="chart-section" aria-labelledby="cost-chart-title">
      <h2 id="cost-chart-title">Cost over time</h2>
      <div class="legend">
        {@render costLegend()}
      </div>
      <TimeChart buckets={data.buckets} kind="cost" {range} selection={bucket} onSelect={(value) => update('bucket', bucket === value ? '' : value)} />
    </section>
    <section class="chart-section" aria-labelledby="token-chart-title">
      <h2 id="token-chart-title">Tokens over time</h2>
      <div class="legend">
        <span><i class="input-color"></i>Uncached input</span>
        <span><i class="cache-color"></i>Cached input</span>
        <span><i class="output-color"></i>Output</span>
      </div>
      <TimeChart buckets={data.buckets} kind="tokens" {range} selection={bucket} onSelect={(value) => update('bucket', bucket === value ? '' : value)} />
    </section>
  </div>

  <div class="secondary-row" class:with-allowance={data.allowance.length > 0}>
    <section aria-labelledby="ranking-title">
      <header class="section-head">
        <h2 id="ranking-title">Usage breakdown</h2>
        <div class="rank-controls">
          <SegmentedControl class="segmented small" aria-label="Group usage by" value={ranking} onValueChange={(value) => { if (value) ranking = value; }}>
            <SegmentedControlItem value="models">Model</SegmentedControlItem>
            <SegmentedControlItem value="clients">Client</SegmentedControlItem>
          </SegmentedControl>
          <SegmentedControl class="segmented small" aria-label="Breakdown unit" value={unit} onValueChange={(value) => { if (value) unit = value; }}>
            <SegmentedControlItem value="dollars">USD</SegmentedControlItem>
            <SegmentedControlItem value="tokens">Tokens</SegmentedControlItem>
          </SegmentedControl>
        </div>
      </header>
      {#if groups.length}
        {#if unit === 'dollars'}
          <div class="legend rank-legend">
            {@render costLegend()}
          </div>
        {/if}
        <div class="rank-list">
          {#each groups as group (group.id)}
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
                  {#if unit === 'tokens'}
                    <span class="input-color" style:width={(100 * group.tokens / maxRank) + '%'}></span>
                  {:else}
                    <span class="api-color" style:width={(100 * group.apiCost / maxRank) + '%'}></span>
                    <span class="codex-color" style:width={(100 * group.codexValue / maxRank) + '%'}></span>
                  {/if}
                {/if}
              </span>
              <strong>{unit === 'tokens' ? compactCount(group.tokens) : money(groupValue(group))}</strong>
            </button>
          {/each}
        </div>
      {:else}
        <div class="empty-state compact-empty">No usage to rank in this view.</div>
      {/if}
    </section>

    {#if data.allowance.length}
      <section class="allowance-section" aria-labelledby="allowance-title">
        <header class="section-head">
          <h2 id="allowance-title">Codex allowance</h2>
          <Clock3Icon size={16} aria-hidden="true" />
        </header>
        {#each data.allowance as allowance (allowance.id)}
          <div class="allowance-row">
            <div class="allowance-heading"><strong>{allowance.label}</strong><span>{Math.round(allowance.remainingPercent)}% left</span></div>
            <Meter value={allowance.remainingPercent} aria-label={allowance.label + ' remaining'} class="allowance-meter">
              <MeterTrack class="allowance-track"><MeterIndicator class="allowance-indicator" /></MeterTrack>
            </Meter>
            {#if allowance.resetsAt}<span class="muted">Resets {formatDateTime(allowance.resetsAt)}</span>{/if}
          </div>
        {/each}
      </section>
    {/if}
  </div>

  {#if bucket}
    <div class="bucket-filter">
      Requests for {formatDateTime(bucket)}
      <Button class="icon-button" aria-label="Clear chart time filter" onclick={() => update('bucket', '')}><XIcon size={14} aria-hidden="true" /></Button>
    </div>
  {/if}
  <RequestList requests={requestPage?.requests} {loading} {canPrevious} canNext={Boolean(requestPage?.nextCursor)} {onPrevious} {onNext} />
{/if}

<style>
  .update-line { display: flex; align-items: center; gap: 7px; font-size: 12px; }
  .metrics, .chart-row, .secondary-row { display: grid; border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .metrics { grid-template-columns: repeat(3, minmax(0, 1fr)); padding-block-end: 6px; }
  .metric { display: flex; flex-direction: column; align-items: flex-start; padding-inline-end: var(--section-space); }
  .metric + .metric, .chart-section + .chart-section, .allowance-section { border-inline-start: 1px solid var(--line); padding-inline-start: var(--section-space); }
  .metric-label { color: var(--muted); font-size: 12px; }
  .metric strong { display: block; margin: 4px 0 2px; font-size: clamp(22px, 3.2vw, 36px); font-weight: 620; letter-spacing: -.05em; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .metric-caption { color: var(--muted); font-size: 11px; }
  .chart-row { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .chart-section { min-inline-size: 0; }
  .chart-section:first-child { padding-inline-end: var(--section-space); }
  .legend { display: flex; gap: 13px; align-items: center; flex-wrap: wrap; font-size: 11px; color: var(--muted); margin: 8px 0 17px; }
  .legend span { display: inline-flex; gap: 5px; align-items: center; white-space: nowrap; }
  .legend i { width: 8px; height: 8px; border-radius: 2px; }
  .secondary-row.with-allowance { grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr); gap: var(--section-space); }
  .rank-controls { display: flex; gap: 6px; align-items: center; }
  .rank-legend { margin-block: 0 8px; }
  .rank-list { display: grid; grid-template-columns: minmax(92px, 1fr) minmax(50px, 3.2fr) minmax(48px, auto); gap: 4px 11px; }
  .rank-item { display: grid; grid-column: 1 / -1; grid-template-columns: subgrid; align-items: center; text-align: start; padding: 6px 0; background: none; border: 0; cursor: pointer; border-radius: 5px; }
  .rank-item:is(:hover, [aria-pressed=true]):not(:disabled) { background: var(--hover); }
  .rank-item:disabled { cursor: default; }
  .rank-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
  .rank-track { background: var(--line); border-radius: 3px; display: flex; block-size: 10px; overflow: hidden; }
  .rank-item strong { font-size: 12px; text-align: end; font-weight: 540; font-variant-numeric: tabular-nums; }
  .compact-empty { min-height: 73px; padding: 12px; }
  .allowance-section .section-head :global(svg) { color: var(--muted); }
  .allowance-row { margin-block-end: 14px; display: grid; gap: 5px; font-size: 11px; }
  .allowance-heading { display: flex; align-items: baseline; justify-content: space-between; font-size: 12px; gap: 8px; }
  .allowance-heading strong { font-weight: 540; }
  .allowance-heading span { color: var(--codex-light); white-space: nowrap; }
  :global(.allowance-track) { background: var(--line); border-radius: 4px; overflow: hidden; block-size: 6px; }
  :global(.allowance-indicator) { display: block; block-size: 100%; background: var(--codex); width: var(--meter-value, 0%); }
  .bucket-filter { display: flex; gap: 10px; align-items: center; color: var(--muted); font-size: 12px; margin-bottom: calc(var(--gap) * -.55); }
  @media (max-width: 900px) {
    .chart-row, .secondary-row.with-allowance { grid-template-columns: minmax(0, 1fr); gap: var(--section-space); }
    .chart-section:first-child { padding-inline-end: 0; }
    .chart-section + .chart-section, .allowance-section { border-inline-start: 0; border-block-start: 1px solid var(--line); padding: var(--section-space) 0 0; }
  }
  @media (max-width: 600px) {
    .metrics { grid-template-columns: 1fr; padding-block: 0; }
    .metric { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 3px 10px; padding: 10px 0; align-items: baseline; }
    .metric + .metric { border-inline-start: 0; border-block-start: 1px solid var(--line); padding-inline-start: 0; }
    .metric strong { margin: 0; grid-column: 2; grid-row: 1 / span 2; align-self: center; }
    .metric-caption { grid-column: 1; }
    .rank-controls { flex-wrap: wrap; justify-content: flex-end; }
  }
</style>
