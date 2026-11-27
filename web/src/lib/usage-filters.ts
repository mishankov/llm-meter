import type { RequestOutcome } from './api';

export const defaultFilters = {
  range: '24h', from: '', to: '', source: [] as string[], provider: [] as string[], client: [] as string[], model: [] as string[], reasoning: [] as string[],
  bucket: '', outcome: '' as RequestOutcome
};

export type UsageFilters = typeof defaultFilters;
export type UsageFilterChange = <Name extends keyof UsageFilters>(name: Name, value: UsageFilters[Name]) => void;

const isoTimestamp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const maxCustomSpanMs = 366 * 86_400_000;

export function hasCustomDateRange(filters: Pick<UsageFilters, 'from' | 'to'>) {
  return Boolean(filters.from && filters.to);
}

export function customBucketIntervalMs(fromMs: number, toMs: number) {
  const span = toMs - fromMs;
  if (span <= 30 * 60_000) return 60_000;
  if (span <= 2 * 3_600_000) return 5 * 60_000;
  if (span <= 6 * 3_600_000) return 15 * 60_000;
  if (span <= 48 * 3_600_000) return 3_600_000;
  if (span <= 7 * 86_400_000) return 6 * 3_600_000;
  return 86_400_000;
}

export function expectedBucketIntervalMs(filters: Pick<UsageFilters, 'range' | 'from' | 'to'>) {
  if (filters.from && filters.to) {
    const fromMs = Date.parse(filters.from);
    const toMs = Date.parse(filters.to);
    if (Number.isFinite(fromMs) && Number.isFinite(toMs) && toMs > fromMs) {
      return customBucketIntervalMs(fromMs, toMs);
    }
  }
  if (filters.range === '1h') return 5 * 60_000;
  if (filters.range === '24h') return 3_600_000;
  return 86_400_000;
}

export function overviewParams(filters: UsageFilters) {
  const query = new URLSearchParams();
  if (filters.from && filters.to) {
    query.set('from', filters.from);
    query.set('to', filters.to);
  } else {
    query.set('range', filters.range);
  }
  for (const name of ['source', 'provider', 'client', 'model', 'reasoning'] as const) {
    for (const value of filters[name]) query.append(name, value);
  }
  return query;
}

export function requestParams(filters: UsageFilters) {
  const query = overviewParams(filters);
  if (filters.bucket) query.set('bucket', filters.bucket);
  if (filters.outcome) query.set('outcome', filters.outcome);
  return query;
}

export function usageURL(filters: UsageFilters) {
  const query = requestParams(filters);
  if (!filters.from && !filters.to && filters.range === defaultFilters.range) query.delete('range');
  return query.size ? '/usage?' + query.toString() : '/usage';
}

export function parseUsageFilters(params: URLSearchParams): UsageFilters {
  const range = params.get('range');
  const outcome = params.get('outcome');
  const rawFrom = params.get('from') ?? '';
  const rawTo = params.get('to') ?? '';
  let from = '';
  let to = '';
  if (isoTimestamp.test(rawFrom) && isoTimestamp.test(rawTo)) {
    const fromMs = Date.parse(rawFrom);
    const toMs = Date.parse(rawTo);
    const span = toMs - fromMs;
    if (Number.isFinite(fromMs) && Number.isFinite(toMs) && span >= 60_000 && span <= maxCustomSpanMs) {
      from = new Date(fromMs).toISOString();
      to = new Date(toMs).toISOString();
    }
  }
  const filters: UsageFilters = {
    range: range === '1h' || range === '7d' || range === '30d' ? range : defaultFilters.range,
    from,
    to,
    source: [...new Set(params.getAll('source').filter((source) => source === 'api' || source === 'codex'))],
    provider: filterText(params.getAll('provider'), 100),
    client: filterText(params.getAll('client'), 100),
    model: filterText(params.getAll('model'), 500),
    reasoning: filterText(params.getAll('reasoning'), 64),
    bucket: params.get('bucket') ?? '',
    outcome: outcome === 'succeeded' || outcome === 'failed' ? outcome : ''
  };
  const interval = expectedBucketIntervalMs(filters);
  if (!isoTimestamp.test(filters.bucket) || Date.parse(filters.bucket) % interval !== 0) filters.bucket = '';
  return filters;
}

const encoder = new TextEncoder();

function filterText(values: string[], maxBytes: number) {
  return [...new Set(values.filter((value) => value && encoder.encode(value).length <= maxBytes))];
}
