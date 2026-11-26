export type Source = 'api' | 'codex';
export type RequestOutcome = '' | 'succeeded' | 'failed';
export const UNREPORTED_REASONING = 'unreported';

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
  lastRequest?: { at: string; status: number; usageAvailable: boolean };
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
  reasoningLevel?: string;
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

export interface ReasoningMixEntry {
  level: string;
  share: number;
}

export interface UsageGroup {
  id: string;
  name: string;
  estimatedCost: number;
  tokens: number;
  requestCount?: number;
  pricedRequestCount?: number;
  reasoningMix?: ReasoningMixEntry[];
  transport?: string;
}

export interface GroupedUsageBucket {
  start: string;
  id: string;
  tokens: number;
  estimatedCost: number;
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
  groupedBuckets?: { models: GroupedUsageBucket[]; clients: GroupedUsageBucket[] };
  groups: { models: UsageGroup[]; clients: UsageGroup[]; reasoning?: UsageGroup[] };
  allowance: Allowance[];
  filters: { models: string[]; clients: Pick<Client, 'id' | 'name'>[]; reasoning?: string[] };
}

export class APIError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

export interface DashboardProblem {
  title: string;
  detail: string;
}

export function dashboardProblem(cause: unknown, title: string): DashboardProblem {
  if (typeof navigator !== 'undefined' && !navigator.onLine) {
    return { title: 'You’re offline', detail: 'Reconnect to the network that hosts your proxy, then retry.' };
  }
  if ((cause instanceof DOMException || cause instanceof Error) && cause.name === 'TimeoutError') {
    return { title: 'The proxy took too long to respond', detail: 'Check that the proxy can respond at this address, then retry.' };
  }
  if (!(cause instanceof APIError)) {
    return { title, detail: 'Check that the proxy is running and that this dashboard address is reachable, then retry.' };
  }
  if (cause.status === 400) {
    return { title, detail: cause.message === 'Could not complete the request.' ? 'Check your selection or input, then retry.' : cause.message };
  }
  if (cause.status === 403) return { title: 'Access was denied', detail: 'Open the dashboard at the proxy’s own address and unlock it with its dashboard token.' };
  if (cause.status === 404) return { title, detail: 'Refresh the dashboard to check whether this item or view is still available.' };
  if (cause.status === 429) return { title: 'The dashboard received too many requests', detail: 'Wait briefly, then retry.' };
  return { title, detail: 'Check the LLM Meter service and its logs. If you use an ingress, verify it can reach the server, then retry.' };
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch('/api' + path, { cache: 'no-store', ...options });
  if (!response.ok) {
    let message: string | undefined;
    if (response.headers.get('Content-Type')?.includes('json')) {
      const data = await response.json().catch(() => null);
      if (typeof data?.error === 'string') message = data.error;
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
