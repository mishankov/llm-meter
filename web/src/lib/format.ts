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
export function formatTimeWindow(value: string, hours: number) {
  const start = new Date(value);
  return dateAndTime.formatRange(start, new Date(start.getTime() + hours * 3_600_000));
}

export const uncachedInput = (value: TokenCounts) =>
  Math.max(0, value.inputTokens - value.cachedInputTokens);
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
