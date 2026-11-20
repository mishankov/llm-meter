import type { TokenCounts } from './api';

const integer = new Intl.NumberFormat();
const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const dollars = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD' });
const preciseDollars = new Intl.NumberFormat(undefined, {
  style: 'currency', currency: 'USD', maximumFractionDigits: 6
});
const cents = new Intl.NumberFormat(undefined, {
  style: 'currency', currency: 'USD', minimumFractionDigits: 0, maximumFractionDigits: 2
});
const time = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' });
const calendar = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });
const dateAndTime = new Intl.DateTimeFormat(undefined, {
  month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'
});

export const count = (n: number) => integer.format(n);
export const compactCount = (n: number) => compact.format(n);
export const money = (n: number) => n > 0 && n < 0.01 ? preciseDollars.format(n) : dollars.format(n);
export const axisMoney = (n: number) => n > 0 && n < 0.01 ? preciseDollars.format(n) : cents.format(n);
export const formatTime = (value: string) => time.format(new Date(value));
export const formatDay = (value: string) => calendar.format(new Date(value));
export const formatDateTime = (value: string) => dateAndTime.format(new Date(value));

export const uncachedInput = (value: TokenCounts) =>
  Math.max(0, value.inputTokens - value.cachedInputTokens);
export const tokens = (value: TokenCounts) => value.inputTokens + value.outputTokens;

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
