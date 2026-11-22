import type { RequestOutcome } from './api';

export const defaultFilters = {
  range: '24h', source: [] as string[], client: [] as string[], model: [] as string[],
  bucket: '', outcome: '' as RequestOutcome
};

export type UsageFilters = typeof defaultFilters;
export type UsageFilterChange = <Name extends keyof UsageFilters>(name: Name, value: UsageFilters[Name]) => void;

export function overviewParams(filters: UsageFilters) {
  const query = new URLSearchParams({ range: filters.range });
  for (const name of ['source', 'client', 'model'] as const) {
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
  if (filters.range === defaultFilters.range) query.delete('range');
  return query.size ? '/usage?' + query.toString() : '/usage';
}

export function parseUsageFilters(params: URLSearchParams): UsageFilters {
  const range = params.get('range');
  const outcome = params.get('outcome');
  const filters: UsageFilters = {
    range: range === '7d' || range === '30d' ? range : defaultFilters.range,
    source: [...new Set(params.getAll('source').filter((source) => source === 'api' || source === 'codex'))],
    client: filterText(params.getAll('client'), 100),
    model: filterText(params.getAll('model'), 500),
    bucket: params.get('bucket') ?? '',
    outcome: outcome === 'succeeded' || outcome === 'failed' ? outcome : ''
  };
  const interval = (filters.range === '24h' ? 1 : 24) * 3_600_000;
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(filters.bucket) ||
    Date.parse(filters.bucket) % interval !== 0) filters.bucket = '';
  return filters;
}

const encoder = new TextEncoder();

function filterText(values: string[], maxBytes: number) {
  return [...new Set(values.filter((value) => value && encoder.encode(value).length <= maxBytes))];
}
