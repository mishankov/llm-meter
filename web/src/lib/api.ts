export type Source = 'api' | 'codex';

export interface Key {
  id: string;
  prefix: string;
  createdAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
}

export interface NewKey extends Key {
  token: string;
}

export interface Client {
  id: string;
  name: string;
  createdAt: string;
  keys: Key[];
}

export interface TokenCounts {
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
  cacheWriteTokens?: number;
}

export interface Rates {
  input: number | null;
  cachedInput: number | null;
  output: number | null;
  cacheWrite: number | null;
  cacheWrite12h?: number | null;
}

export interface UsageRequest extends TokenCounts {
  id: number;
  at: string;
  source: Source;
  provider?: string;
  clientId: string;
  client: string;
  model: string;
  endpoint: string;
  method: string;
  status: number;
  usageAvailable: boolean;
  cost: number | null;
  rates: Rates | null;
  cacheWrite12hTokens?: number;
  priceSource?: string;
  reportedListCost?: number;
  reportedAllowance?: number;
}

export interface UsageBucket extends TokenCounts {
  start: string;
  estimatedCost: number;
  requestCount?: number;
  pricedRequestCount?: number;
}

export interface UsageGroup {
  id: string;
  name: string;
  estimatedCost: number;
  tokens: number;
  requestCount?: number;
  pricedRequestCount?: number;
}

export interface Allowance {
  id: string;
  label: string;
  remainingPercent: number;
  resetsAt: string | null;
}

export interface RequestPage {
  requests: UsageRequest[];
  nextCursor: string | null;
}

export interface Usage extends RequestPage {
  summary: Omit<UsageBucket, 'start'> & { requestCount: number; pricedRequestCount: number };
  buckets: UsageBucket[];
  groups: { models: UsageGroup[]; clients: UsageGroup[] };
  allowance: Allowance[];
  filters: { models: string[]; clients: Pick<Client, 'id' | 'name'>[] };
}

export class APIError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch('/api' + path, { cache: 'no-store', ...options });
  if (!response.ok) {
    let message: string | undefined;
    if (response.headers.get('Content-Type')?.includes('json')) {
      const data = await response.json();
      if (typeof data.error === 'string') message = data.error;
    }
    throw new APIError(response.status, message ?? 'Could not complete the request.');
  }
  if (response.status === 204) return undefined as T;
  return response.json();
}

export function jsonPost(data: object): RequestInit {
  return {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  };
}
