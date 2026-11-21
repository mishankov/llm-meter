import type { TokenCounts, UsageBucket } from './api';

const integer = new Intl.NumberFormat();
const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const dollars = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD' });
const preciseDollars = new Intl.NumberFormat(undefined, {
  style: 'currency', currency: 'USD', maximumFractionDigits: 6
});
const time = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' });
const calendar = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });
const utcCalendar = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
const dateAndTime = new Intl.DateTimeFormat(undefined, {
  month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'
});

export const count = (n: number) => integer.format(n);
export const compactCount = (n: number) => compact.format(n);
export function money(n: number) {
  if (n > 0 && n < 0.000001) return '<' + preciseDollars.format(0.000001);
  return n > 0 && n < 0.01 ? preciseDollars.format(n) : dollars.format(n);
}

export function axisMoneyFormatter(step: number) {
  // The tick step may include a half-unit (2.5 × a power of ten).
  const maximumFractionDigits = Math.min(20, Math.max(0, 1 - Math.floor(Math.log10(step))));
  return new Intl.NumberFormat(undefined, {
    style: 'currency', currency: 'USD', minimumFractionDigits: 0, maximumFractionDigits
  }).format;
}
export const formatTime = (value: string) => time.format(new Date(value));
export const formatDay = (value: string) => calendar.format(new Date(value));
export const formatUTCDay = (value: string) => utcCalendar.format(new Date(value));
export const formatDateTime = (value: string) => dateAndTime.format(new Date(value));

export function requestStatusLabel(status: number) {
  if (status === 0) return 'Interrupted';
  if (status === 101) return 'WebSocket connected';
  if (status < 200) return 'Informational response';
  if (status < 300) return 'Response received';
  if (status >= 300 && status < 400) return 'Redirected';
  if (status === 400) return 'Invalid request';
  if (status === 401) return 'Authentication failed';
  if (status === 403) return 'Access denied';
  if (status === 404) return 'Model or endpoint not found';
  if (status === 429) return 'Rate limited';
  return status >= 500 ? 'Server error' : 'Request failed';
}

export function requestStatusGuidance(status: number) {
  if (status === 0) return 'Check the client and proxy logs for a connection that closed before the request completed.';
  if (status === 401 || status === 403) return 'Check the credentials your client uses with the provider and that they can access this model.';
  if (status === 404) return 'Check the base URL and model configured in your client.';
  if (status === 429) return 'Check the provider’s rate limit or remaining allowance before trying the request again.';
  if (status >= 500) return 'Check the provider status and the proxy logs, then retry from your client.';
  return 'Check the model and request settings in your client, then send a new request.';
}

export function formatTimeWindow(value: string, hours: number) {
  const start = new Date(value);
  return dateAndTime.formatRange(start, new Date(start.getTime() + hours * 3_600_000));
}

export const uncachedInput = (value: TokenCounts) =>
  Math.max(0, value.inputTokens - value.cachedInputTokens - (value.cacheWriteTokens ?? 0));
export const tokens = (value: TokenCounts) => value.inputTokens + value.outputTokens;
export const unpricedBucket = (value: UsageBucket) => (value.requestCount ?? 0) > 0 && value.pricedRequestCount === 0;

export function partialPricing({ requestCount, pricedRequestCount }: { requestCount?: number; pricedRequestCount?: number }) {
  return pricedRequestCount && requestCount && pricedRequestCount < requestCount
    ? `${count(pricedRequestCount)} of ${count(requestCount)} priced`
    : '';
}

export function dismissShortcut(event: KeyboardEvent) {
  const target = event.target;
  return event.repeat || event.isComposing || event.keyCode === 229 ||
    event.metaKey || event.ctrlKey || event.altKey || event.shiftKey ||
    (target instanceof HTMLElement && Boolean(target.closest('input, textarea, select, [contenteditable], [role="dialog"]')));
}

export function guardEnter(event: KeyboardEvent) {
  if (event.key === 'Enter' && (event.repeat || event.isComposing || event.keyCode === 229)) {
    event.preventDefault();
  }
}
