import { expect, test, type Page } from '@playwright/test';
import type { Client, Key, Usage, UsageRequest } from '../src/lib/api';

const initialStart = '2026-11-20T12:00:00Z';
const rates = { input: 1, cachedInput: .1, output: 2, cacheWrite: null };
const response: Usage = {
  summary: { estimatedCost: .016, inputTokens: 360, cachedInputTokens: 60, cacheWriteTokens: 100, outputTokens: 40, requestCount: 4, pricedRequestCount: 2 },
  buckets: [
    { start: initialStart, estimatedCost: 0, inputTokens: 300, cachedInputTokens: 60, cacheWriteTokens: 100, outputTokens: 30, requestCount: 2, pricedRequestCount: 1 },
    { start: '2026-11-20T13:00:00Z', estimatedCost: .016, inputTokens: 60, cachedInputTokens: 0, outputTokens: 10, requestCount: 1, pricedRequestCount: 1 },
    { start: '2026-11-20T14:00:00Z', estimatedCost: 0, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, requestCount: 1, pricedRequestCount: 0 }
  ],
  groups: {
    models: [
      { id: 'gpt-api', name: 'gpt-api', estimatedCost: 0, tokens: 330, pricedRequestCount: 1 },
      { id: 'gpt-codex', name: 'gpt-codex', estimatedCost: .016, tokens: 70 },
      { id: 'gpt-6-luna', name: 'gpt-6-luna', estimatedCost: .001346, tokens: 10 },
      { id: 'gpt-unpriced', name: 'gpt-unpriced', estimatedCost: 0, tokens: 0, requestCount: 2, pricedRequestCount: 0 }
    ],
    clients: [{ id: 'open-code', name: 'OpenCode', estimatedCost: 0, tokens: 330, requestCount: 3, pricedRequestCount: 1 }]
  },
  requests: [
    { id: 1, at: '2026-11-20T12:12:00Z', source: 'api', provider: 'opencode-go', clientId: 'open-code', client: 'OpenCode', model: 'gpt-api', endpoint: '/opencode-go/responses', method: 'POST', status: 200, usageAvailable: true, inputTokens: 300, cachedInputTokens: 60, cacheWriteTokens: 100, outputTokens: 30, cost: 0, rates: null, priceSource: 'provider-reported', reportedListCost: .045, reportedAllowance: .018 },
    { id: 2, at: '2026-11-20T13:02:00Z', source: 'codex', clientId: '', client: '', model: 'gpt-codex', endpoint: '/codex/responses', method: 'WS', status: 200, usageAvailable: true, inputTokens: 60, cachedInputTokens: 0, outputTokens: 10, cost: .016, rates },
    { id: 3, at: '2026-11-20T14:03:00Z', source: 'api', provider: 'google', clientId: 'open-code', client: 'OpenCode', model: 'gpt-unpriced', endpoint: '/antigravity/v1internal:streamGenerateContent', method: 'POST', status: 200, usageAvailable: true, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, cost: null, rates: null },
    { id: 4, at: '2026-11-20T12:13:00Z', source: 'api', clientId: 'open-code', client: 'OpenCode', model: 'gpt-unpriced', endpoint: '/v1/responses', method: 'POST', status: 200, usageAvailable: true, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, cost: null, rates: null }
  ],
  allowance: [{ id: 'open-code-window', label: '5-hour window', remainingPercent: 77, resetsAt: '2026-11-20T16:00:00Z' }],
  filters: { models: ['gpt-api', 'gpt-codex'], clients: [{ id: 'open-code', name: 'OpenCode' }] },
  nextCursor: null
};

async function mockAPI(page: Page, {
  empty = false, initialClients = [], extraRequests = [], groupedBuckets
}: { empty?: boolean; initialClients?: Client[]; extraRequests?: UsageRequest[]; groupedBuckets?: Usage['groupedBuckets'] } = {}) {
  let authorized = false;
  let keyCount = 0;
  const clients = [...initialClients];
  const usageQueries: URLSearchParams[] = [];
  const usagePaths: string[] = [];
  const createKey = () => {
    keyCount++;
    const metadata: Key = {
      id: String(keyCount), prefix: 'test_key_' + keyCount,
      createdAt: initialStart, lastUsedAt: null, revokedAt: null
    };
    return { metadata, created: { ...metadata, token: metadata.prefix + '_secret' } };
  };

  await page.route('**/api/**', async (route) => {
    const http = route.request();
    const method = http.method();
    const url = new URL(http.url());
    if (url.pathname === '/api/login') {
      authorized = http.postDataJSON().token === 'dashboard-token';
      return route.fulfill({ status: authorized ? 204 : 401 });
    }
    if (!authorized) return route.fulfill({ status: 401 });
    if (url.pathname === '/api/session') return route.fulfill({ status: 204 });
    if (url.pathname === '/api/logout') {
      authorized = false;
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === '/api/usage' || url.pathname === '/api/usage/requests') {
      usageQueries.push(url.searchParams);
      usagePaths.push(url.pathname);
      const data = empty ? {
        ...response,
        summary: { estimatedCost: 0, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, requestCount: 0, pricedRequestCount: 0 },
        buckets: [], groups: { models: [], clients: [] }, requests: [],
        filters: { models: [], clients: clients.map(({ id, name }) => ({ id, name })) }
      } : {
        ...response,
        groupedBuckets,
        summary: { ...response.summary, requestCount: response.summary.requestCount + extraRequests.length },
        requests: [...response.requests, ...extraRequests]
      };
      const cursor = url.searchParams.get('cursor');
      const bucket = url.searchParams.get('bucket');
      const client = url.searchParams.get('client');
      const source = url.searchParams.get('source');
      const outcome = url.searchParams.get('outcome');
      const days = url.searchParams.get('range') === '30d' ? 31 : url.searchParams.get('range') === '7d' ? 8 : 0;
      const buckets = days && !empty ? Array.from({ length: days }, (_, index) => ({
        ...data.buckets[0],
        start: new Date(Date.UTC(2026, 10, 20 + index)).toISOString()
      })) : data.buckets;
      const bucketStart = bucket ? Date.parse(bucket) : null;
      const requests = data.requests.filter((request) =>
        (bucketStart === null || (Date.parse(request.at) >= bucketStart && Date.parse(request.at) < bucketStart + (days ? 86_400_000 : 3_600_000))) &&
        (!client || request.clientId === client) &&
        (!source || request.source === source) &&
        (outcome !== 'failed' || request.status === 0 || request.status >= 400) &&
        (outcome !== 'succeeded' || (request.status >= 200 && request.status < 300)) &&
        (!cursor || request.source === 'codex')
      );
      const nextCursor = !empty && !cursor && !bucket && !client && !outcome ? 'test-cursor' : null;
      const result = { requests, nextCursor };
      return route.fulfill({ json: url.pathname === '/api/usage' ? { ...data, buckets, ...result } : result });
    }
    if (url.pathname === '/api/clients') {
      if (method === 'GET') return route.fulfill({ json: { clients } });
      const { metadata, created } = createKey();
      const client = { id: 'open-code', name: http.postDataJSON().name, createdAt: initialStart, keys: [metadata] };
      clients.push(client);
      return route.fulfill({ json: { client, key: created } });
    }
    if (url.pathname === '/api/clients/open-code') {
      const index = clients.findIndex(({ id }) => id === 'open-code');
      if (index < 0) return route.fulfill({ status: 404 });
      if (method === 'PATCH') clients[index].name = http.postDataJSON().name;
      else if (method === 'DELETE') clients.splice(index, 1);
      else return route.fulfill({ status: 405 });
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === '/api/clients/open-code/keys') {
      const { metadata, created } = createKey();
      clients[0].keys.push(metadata);
      return route.fulfill({ json: { key: created } });
    }
    if (url.pathname === '/api/clients/open-code/keys/1' && method === 'DELETE') {
      clients[0].keys[0].revokedAt = initialStart;
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ status: 404 });
  });
  return { usageQueries, usagePaths, expireSession: () => { authorized = false; } };
}

async function unlock(page: Page) {
  await page.goto('/');
  const field = page.getByLabel('Access token');
  await field.fill('dashboard-token');
  await field.press('Enter');
  await expect(page.getByRole('heading', { name: 'Usage overview' })).toBeVisible();
}

test('filters requests from a chart and inspects and pages through them with the keyboard', async ({ page }) => {
  const { usageQueries, usagePaths } = await mockAPI(page);
  await unlock(page);
  const metric = page.getByRole('region', { name: 'Usage value', exact: true });
  await expect(metric.getByText('$0.02', { exact: true })).toBeVisible();
  await expect(metric.getByText('2 of 4 requests priced', { exact: true })).toBeVisible();
  const requests = page.getByRole('region', { name: 'Requests' });
  await expect(requests.getByText('Reported cost', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Codex allowance', exact: true })).toHaveCount(0);
  await requests.getByRole('button', { name: 'Show 1 more request' }).press('Enter');
  await expect(requests.getByText('Google', { exact: true }).filter({ visible: true })).toBeVisible();
  await expect(requests.getByText('Unpriced', { exact: true })).toHaveCount(2);
  await page.getByRole('radio', { name: 'Usage value' }).click();
  const chart = page.getByRole('region', { name: 'Usage value over time' });
  const firstBucket = chart.getByRole('button').first();
  const secondBucket = chart.getByRole('button').nth(1);
  await expect(firstBucket).toHaveAccessibleName(/Usage value \$0\.00 · 1 of 2 priced$/);
  await expect(secondBucket).toHaveAccessibleName(/Usage value \$0\.02$/);
  await expect(chart.getByRole('button').last()).toHaveAccessibleName(/Unpriced$/i);
  await expect(secondBucket.locator('.bars > span')).toHaveCount(1);
  const tickLabels = await chart.locator('.chart-axis span').allTextContents();
  expect(tickLabels).toHaveLength(5);
  expect(new Set(tickLabels).size).toBe(tickLabels.length);
  await firstBucket.focus();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Shift+Tab');
  const tooltip = chart.locator('.chart-tooltip');
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toContainText('1 of 2 priced');
  await expect(firstBucket).toHaveAccessibleName(/Usage value \$0\.00 · 1 of 2 priced$/);
  await page.keyboard.press('Escape');
  await expect(tooltip).toBeHidden();
  await expect(firstBucket).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(secondBucket).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(firstBucket).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(firstBucket).toHaveAttribute('aria-pressed', 'true');
  await expect(firstBucket).toBeFocused();
  await expect.poll(() => usageQueries.at(-1)?.get('bucket')).toBe(initialStart);
  expect(usagePaths.at(-1)).toBe('/api/usage/requests');
  const request = page.getByRole('button', { name: /Details for gpt-api/ });
  await request.focus();
  await page.keyboard.press('Enter');
  const detail = page.getByRole('dialog', { name: 'Request details' });
  await expect(detail).toBeVisible();
  await expect(detail).toHaveCSS('height', `${page.viewportSize()!.height}px`);
  await expect(detail.getByText('Reported cost', { exact: true })).toBeVisible();
  await expect(detail.getByText('$0.00', { exact: true })).toBeVisible();
  await expect(detail.getByText('Provider list value', { exact: true })).toBeVisible();
  await expect(detail.getByText('Allowance used', { exact: true })).toBeVisible();
  await expect(detail.getByText('OpenCode Go', { exact: true })).toBeVisible();
  await expect(detail.getByText('Token usage', { exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: 'Request details' })).not.toBeVisible();
  await expect(request).toBeFocused();
  await page.getByRole('button', { name: 'Clear chart time filter' }).click();
  await page.getByRole('button', { name: 'Next requests' }).click();
  await expect.poll(() => usageQueries.at(-1)?.get('cursor')).toBe('test-cursor');
  expect(usagePaths.at(-1)).toBe('/api/usage/requests');
  await page.getByRole('button', { name: /Details for gpt-codex/ }).press('Enter');
  await expect(detail.getByText('Estimated cost', { exact: true })).toBeVisible();
  await expect(detail.getByText('$0.02', { exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('button', { name: 'Previous requests' })).toBeEnabled();
  await page.getByRole('button', { name: 'Previous requests' }).click();
  await expect.poll(() => usageQueries.at(-1)?.get('cursor')).toBeNull();
});

test('keeps horizontal table scrolling inside the dashboard at narrow widths', async ({ page }) => {
  const listValue = { ...response.requests[0], id: 43, model: 'model-list-value', provider: 'openrouter', priceSource: 'provider-list' };
  await mockAPI(page, { extraRequests: [listValue] });
  await unlock(page);
  await page.getByRole('button', { name: /Details for gpt-api/ }).waitFor();

  for (const width of [300, 320, 390, 768]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
    await expect(page.getByRole('button', { name: 'Lock dashboard' })).toBeInViewport({ ratio: 1 });
  }
  await page.setViewportSize({ width: 300, height: 900 });
  await page.getByRole('button', { name: /Show .* more requests/ }).click();
  const provider = page.getByRole('cell', { name: /model-list-value/ }).getByText('OpenRouter', { exact: true });
  const providerFits = await provider.evaluate((element) => {
    const bounds = element.getBoundingClientRect();
    const text = document.createRange();
    text.selectNodeContents(element);
    return Array.from(text.getClientRects()).every((line) => line.right <= bounds.right + 1);
  });
  expect(providerFits).toBe(true);
});

test('keeps long request names compact while exposing cache writes and full details', async ({ page }) => {
  const model = 'org/' + 'model-name-'.repeat(17);
  const client = 'a'.repeat(100);
  const extra = { ...response.requests[0], id: 42, model, client };
  await mockAPI(page, { extraRequests: [extra] });
  await page.setViewportSize({ width: 320, height: 844 });
  await unlock(page);
  await page.getByRole('radiogroup', { name: 'Chart metric' }).getByRole('radio', { name: 'Tokens' }).click();
  const chart = page.getByRole('region', { name: 'Tokens over time' });
  await expect(chart.getByRole('button').first()).toHaveAccessibleName(
    /Uncached input 140, cached input 60, cache writes 100, output 30$/
  );
  await page.getByRole('button', { name: /Show .* more requests/ }).click();
  const row = page.getByRole('row').filter({ hasText: model });
  const detailButton = row.getByRole('button', { name: /Details for/ });
  await detailButton.waitFor();
  expect((await row.boundingBox())?.height).toBeLessThan(150);
  await detailButton.press('Enter');
  const detail = page.getByRole('dialog', { name: 'Request details' });
  await expect(detail.getByText(model, { exact: true })).toBeVisible();
  await expect(detail.getByText(client, { exact: true })).toBeVisible();
  await expect(detail.getByText('30-minute cache writes', { exact: true })).toBeVisible();
});

test('switches between Tokens and Usage value charts with the keyboard', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockAPI(page);
  await unlock(page);
  const metrics = page.getByRole('radiogroup', { name: 'Chart metric' });
  const radios = metrics.getByRole('radio');
  const tokens = metrics.getByRole('radio', { name: 'Tokens', exact: true });
  const value = metrics.getByRole('radio', { name: 'Usage value', exact: true });
  const valueChart = page.getByRole('region', { name: 'Usage value over time' });
  const tokenChart = page.getByRole('region', { name: 'Tokens over time' });
  await expect(radios).toHaveCount(2);
  await expect(radios.first()).toHaveAccessibleName('Tokens');
  await expect(radios.last()).toHaveAccessibleName('Usage value');
  await expect(tokens).toBeChecked();
  await expect(tokenChart).toBeVisible();
  await expect(valueChart).toHaveCount(0);

  await tokens.press('ArrowRight');
  await expect(value).toBeChecked();
  await expect(value).toBeFocused();
  await expect(valueChart).toBeVisible();
  await expect(tokenChart).toHaveCount(0);

  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('link', { name: 'Usage', exact: true }).click();
  await expect(value).toBeChecked();
  await expect(valueChart).toBeVisible();
  await expect(tokenChart).toHaveCount(0);

  await value.press('ArrowLeft');
  await expect(tokens).toBeChecked();
  await expect(tokens).toBeFocused();
  await expect(tokenChart).toBeVisible();
  await expect(valueChart).toHaveCount(0);
  const requests = page.getByRole('region', { name: 'Requests', exact: true });
  await expect(requests.getByText('4 requests on this page')).toBeVisible();
});

test('groups the timeline by models or clients and puts both breakdowns before the request preview', async ({ page }) => {
  await mockAPI(page, { groupedBuckets: {
    models: [
      { start: initialStart, id: 'gpt-api', tokens: 320, estimatedCost: 0 },
      { start: initialStart, id: 'gpt-6-luna', tokens: 10, estimatedCost: 0 }
    ],
    clients: [{ start: initialStart, id: 'open-code', tokens: 330, estimatedCost: 0 }]
  }});
  await unlock(page);
  const chart = page.getByRole('region', { name: 'Tokens over time' });
  const grouping = page.getByRole('radiogroup', { name: 'Group timeline by' });
  const firstBucket = chart.getByRole('button').first();
  await expect(firstBucket).toHaveAccessibleName(/Tokens 330 · gpt-6-luna 10, gpt-api 320$/);
  await page.emulateMedia({ forcedColors: 'active' });
  const backgrounds = (selector: string) => chart.locator(selector).evaluateAll((elements) =>
    elements.map((element) => getComputedStyle(element).backgroundImage));
  const fills = await backgrounds('.chart-column:first-child .group-bar');
  expect(new Set(fills).size).toBe(2);
  expect(await backgrounds('.group-swatch')).toEqual(fills);
  await page.emulateMedia({ forcedColors: 'none' });
  await grouping.getByRole('radio', { name: 'Models' }).press('ArrowRight');
  await expect(grouping.getByRole('radio', { name: 'Clients' })).toBeChecked();
  await expect(chart.getByRole('button').first()).toHaveAccessibleName(/Tokens 330 · OpenCode 330$/);

  const breakdown = page.getByRole('region', { name: 'Period totals and shares' });
  const model = breakdown.getByRole('row', { name: /gpt-codex/ });
  await expect(model.getByText('$0.02', { exact: true })).toBeVisible();
  await expect(model.getByText('70', { exact: true })).toBeVisible();
  await expect(model.getByText('17.5%', { exact: true })).toBeVisible();
  await expect(breakdown.getByRole('row', { name: /gpt-unpriced/ }).getByText('Unpriced', { exact: true })).toBeVisible();
  const mixed = breakdown.getByRole('row', { name: /OpenCode/ });
  await expect(mixed.getByText('$0.00', { exact: true })).toBeVisible();
  await expect(mixed.getByText('1 of 3 priced', { exact: true })).toBeVisible();

  const requests = page.getByRole('region', { name: 'Requests', exact: true });
  expect(await requests.evaluate((node) => {
    const period = document.getElementById('breakdown-title');
    return Boolean(period && period.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING);
  })).toBe(true);
  const rows = requests.locator('tbody tr');
  const more = requests.getByRole('button', { name: 'Show 1 more request' });
  await expect(rows).toHaveCount(3);
  await expect(requests.getByText('Showing 3 of 4 requests on this page')).toBeVisible();
  await page.getByRole('button', { name: 'View requests' }).press('Enter');
  await expect(requests.getByRole('heading', { name: 'Requests' })).toBeFocused();
  await more.press('Enter');
  await expect(rows).toHaveCount(4);
  await expect(rows.nth(3).getByRole('button', { name: /Details/ })).toBeFocused();
  const fewer = requests.getByRole('button', { name: 'Show fewer requests' });
  await page.keyboard.press('Tab');
  await expect(fewer).toBeFocused();
  await fewer.press('Space');
  await expect(rows).toHaveCount(3);
  await expect(more).toBeFocused();
});

test('aligns breakdown amounts and keeps model filters keyboard-operable', async ({ page }) => {
  const { usageQueries } = await mockAPI(page);
  await unlock(page);
  const breakdown = page.getByRole('region', { name: 'By model' });
  const amounts = breakdown.locator('tbody td:nth-child(2)');
  await expect(amounts).toHaveCount(4);

  for (const width of [1280, 320]) {
    await page.setViewportSize({ width, height: 900 });
    const columns = await amounts.evaluateAll((elements) => elements.map((element) => {
      const { left, right } = element.getBoundingClientRect();
      return { left, right };
    }));
    expect(columns).toEqual(columns.map(() => columns[0]));
  }

  const luna = breakdown.getByRole('button', { name: /Filter by model gpt-6-luna/ });
  const model = page.getByRole('combobox', { name: 'Model' });
  await luna.press('Enter');
  await expect(luna).toHaveAttribute('aria-pressed', 'true');
  await expect(model).toBeFocused();
  await expect(model).toBeInViewport();
  await expect.poll(() => usageQueries.at(-1)?.get('model')).toBe('gpt-6-luna');
  await luna.press('Space');
  await expect(luna).toHaveAttribute('aria-pressed', 'false');
  await expect(model).toBeFocused();
  await expect.poll(() => usageQueries.at(-1)?.get('model')).toBeNull();
});

test('hides previous results when loading the selected source fails', async ({ page }) => {
  await mockAPI(page);
  let failFilteredQuery = true;
  let finishLoading!: () => void;
  const pending = new Promise<void>((resolve) => { finishLoading = resolve; });
  await page.route('**/api/usage?**', async (route) => {
    const source = new URL(route.request().url()).searchParams.get('source');
    if (source === 'codex' && failFilteredQuery) {
      await pending;
      return route.fulfill({ status: 503 });
    }
    return route.fallback();
  });
  await unlock(page);
  const apiRequest = page.getByRole('button', { name: /Details for gpt-api/ });
  await expect(apiRequest).toBeVisible();
  await page.getByRole('combobox', { name: 'Source' }).click();
  await page.getByRole('option', { name: 'Codex subscription' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Loading usage…' })).toBeVisible();
  await expect(apiRequest).not.toBeVisible();
  finishLoading();
  await expect(page.getByRole('alert')).toContainText('Could not load usage');
  await expect(page.getByRole('alert')).toContainText('Check the LLM Meter service and its logs.');
  await expect(apiRequest).not.toBeVisible();
  await expect(page.getByRole('region', { name: 'Usage value over time' })).not.toBeVisible();

  failFilteredQuery = false;
  await page.getByRole('button', { name: 'Retry usage' }).press('Enter');
  await expect(page.getByRole('button', { name: /Details for gpt-codex/ })).toBeVisible();
  await expect(apiRequest).not.toBeVisible();
});

test('shows stale data during an outage and refreshes the same scope on reconnect', async ({ context, page }) => {
  const { usageQueries } = await mockAPI(page);
  let failRefresh = false;
  await page.route('**/api/usage?**', (route) => failRefresh ? route.fulfill({ status: 503 }) : route.fallback());
  await unlock(page);
  const total = page.getByRole('region', { name: 'Usage value', exact: true });
  await expect(total.getByText('$0.02')).toBeVisible();
  await page.getByRole('radio', { name: '7 days' }).click();
  await expect(total.getByText('$0.02')).toBeVisible();

  await context.setOffline(true);
  await expect(page.getByText(/Offline · Last updated/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Refresh usage' })).toBeDisabled();
  await expect(total.getByText('$0.02')).toBeVisible();

  failRefresh = true;
  await context.setOffline(false);
  await expect(page.getByText(/Refresh failed · Last updated/)).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Showing the last loaded data for these filters.');
  await expect(total.getByText('$0.02')).toBeVisible();

  await page.getByRole('button', { name: 'Next requests' }).press('Enter');
  await expect(page.getByRole('button', { name: /Details for gpt-codex/ })).toBeVisible();
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  await expect(page.getByText(/Refresh failed · Last updated/)).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Showing the last loaded data for these filters.');

  await page.getByRole('radio', { name: '30 days' }).click();
  await expect(page.getByRole('alert')).toContainText('Could not load usage');
  await expect(total).toHaveCount(0);
  await page.getByRole('radio', { name: '7 days' }).click();
  await expect(total.getByText('$0.02')).toBeVisible();
  await expect(page.getByText(/Refresh failed · Last updated/)).toBeVisible();
  const next = page.getByRole('button', { name: 'Next requests' });
  await expect(next).toBeEnabled();
  await next.press('Enter');
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();

  failRefresh = false;
  const retry = page.getByRole('button', { name: 'Retry usage' });
  await expect(retry).toBeEnabled();
  await retry.press('Enter');
  await expect(page.getByRole('button', { name: 'Refresh usage' })).toBeFocused();
  await expect(page.getByRole('alert')).toHaveCount(0);
  expect(usageQueries.at(-1)?.get('range')).toBe('7d');
  expect(usageQueries.at(-1)?.get('cursor')).toBe('test-cursor');
});

test('retries failed request pages in place and links client diagnostics to the same outcome filter', async ({ page }) => {
  const failed: UsageRequest = {
    ...response.requests[0], id: 5, model: 'gpt-rate-limited', status: 429, at: '2026-11-20T14:06:00Z',
    usageAvailable: false, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, cost: null, priceSource: undefined
  };
  const websocket: UsageRequest = { ...failed, id: 6, model: 'gpt-websocket', status: 101 };
  const { usagePaths, usageQueries } = await mockAPI(page, {
    extraRequests: [failed, websocket],
    initialClients: [{
      id: 'open-code', name: 'OpenCode', createdAt: initialStart,
      keys: [{ id: '1', prefix: 'test_key_1', createdAt: initialStart, lastUsedAt: failed.at, revokedAt: null }],
      lastRequest: { at: failed.at, status: failed.status, usageAvailable: false }
    }]
  });
  let rejectRequests = true;
  await page.route('**/api/usage/requests?**', (route) => rejectRequests ? route.fulfill({ status: 429 }) : route.fallback());
  await unlock(page);
  const requests = page.getByRole('region', { name: 'Requests' });
  const amount = page.getByRole('region', { name: 'Usage value', exact: true });
  await expect(amount.getByText('$0.02')).toBeVisible();
  await requests.getByRole('button', { name: /Show .* more requests/ }).click();
  const websocketRow = requests.getByRole('button', { name: /Details for gpt-websocket/ });
  await expect(websocketRow).toBeVisible();
  await requests.getByRole('combobox', { name: 'Request outcome' }).press('Enter');
  await page.getByRole('option', { name: 'HTTP errors or interrupted' }).click();
  await expect(page).toHaveURL(/outcome=failed$/);
  await expect(requests.getByRole('alert')).toContainText('The dashboard received too many requests');
  await expect(amount.getByText('$0.02')).toBeVisible();
  await expect(page.getByRole('button', { name: /Details for gpt-api/ })).toHaveCount(0);

  rejectRequests = false;
  await requests.getByRole('button', { name: 'Retry requests' }).press('Enter');
  const failedRow = requests.getByRole('button', { name: /Details for gpt-rate-limited/ });
  await expect(failedRow).toBeVisible();
  expect(usagePaths.at(-1)).toBe('/api/usage/requests');
  expect(usageQueries.at(-1)?.get('outcome')).toBe('failed');
  await page.reload();
  await expect(failedRow).toBeVisible();
  await failedRow.press('Enter');
  const detail = page.getByRole('dialog', { name: 'Request details' });
  await expect(detail.getByText(/Rate limited · HTTP 429/)).toBeVisible();
  await expect(detail.getByText(/Check the provider’s rate limit/)).toBeVisible();
  await page.keyboard.press('Escape');

  await requests.getByRole('combobox', { name: 'Request outcome' }).press('Enter');
  await page.getByRole('option', { name: 'HTTP 2xx responses' }).click();
  await expect(requests.getByRole('row', { name: /HTTP 200, Response received/ }).first()).toBeVisible();
  await expect(websocketRow).toHaveCount(0);
  await expect(failedRow).toHaveCount(0);

  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('button', { name: 'Manage client OpenCode' }).click();
  await expect(page.getByText('Latest recorded request')).toBeVisible();
  await expect(page.getByText('Rate limited (429)')).toBeVisible();
  const clientType = page.getByRole('combobox', { name: 'Connection type' });
  await expect(page.getByRole('textbox', { name: 'Base URL' })).toHaveValue(/\/v1$/);
  await clientType.press('Enter');
  await page.getByRole('option', { name: 'Codex (ChatGPT sign-in)' }).click();
  const baseURL = new URL('/codex', page.url()).href;
  await expect(page.getByRole('textbox', { name: 'Base URL' })).toHaveValue(baseURL);
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  const copyURL = page.getByRole('button', { name: 'Copy URL', exact: true });
  await expect(copyURL).toHaveAttribute('aria-keyshortcuts', 'Enter');
  await copyURL.press('Enter');
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(baseURL);
  await page.getByRole('button', { name: 'Provider setup instructions' }).press('Enter');
  await expect(page.getByText('config.toml', { exact: true })).toBeVisible();
  await expect(page.getByText('LLM_METER_KEY', { exact: true })).toBeVisible();
  await clientType.press('Enter');
  await page.getByRole('option', { name: 'OpenCode (ChatGPT sign-in)' }).click();
  await expect(page.getByRole('textbox', { name: 'Base URL' })).toHaveValue(baseURL);
  await expect(page.getByText('opencode.jsonc', { exact: true })).toBeVisible();
  await clientType.press('Enter');
  await page.getByRole('option', { name: 'OpenCode Go' }).click();
  await expect(page.getByRole('textbox', { name: 'Base URL' })).toHaveValue(/\/opencode-go$/);
  await page.getByRole('button', { name: 'View failed requests' }).press('Enter');
  await expect(page).toHaveURL(/range=30d&client=open-code&outcome=failed$/);
  await expect(failedRow).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Requests', exact: true })).toBeFocused();
});

test('keeps key shortcuts and repeated Enter from interrupting connection selection', async ({ page }) => {
  await mockAPI(page, {
    initialClients: [{ id: 'open-code', name: 'OpenCode', createdAt: initialStart, keys: [] }]
  });
  let keyRequests = 0;
  await page.route('**/api/clients/open-code/keys', async (route) => {
    if (route.request().method() === 'POST') keyRequests++;
    await route.fallback();
  });
  await unlock(page);
  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('button', { name: 'Manage client OpenCode' }).click();
  const connection = page.getByRole('combobox', { name: 'Connection type' });
  const baseURL = page.getByRole('textbox', { name: 'Base URL' });
  const options = page.getByRole('listbox');
  const newKey = page.getByRole('button', { name: 'New key', exact: true });

  await connection.press('k');
  await connection.dispatchEvent('keydown', { key: 'л', code: 'KeyK' });
  await connection.focus();
  await connection.dispatchEvent('keydown', { key: 'Enter', isComposing: true });
  await expect(options).toBeHidden();
  await page.keyboard.down('Enter');
  await expect(options).toBeVisible();
  await page.keyboard.down('Enter');
  await expect(options).toBeVisible();
  await page.keyboard.press('End');
  await page.keyboard.down('Enter');
  await expect(options).toBeVisible();
  await expect(baseURL).toHaveValue(/\/v1$/);
  await page.keyboard.up('Enter');
  await page.keyboard.press('Enter');
  await expect(options).toBeHidden();
  await expect(baseURL).toHaveValue(/\/opencode-go$/);

  await connection.press('Enter');
  await page.keyboard.press('k');
  await page.evaluate(() => document.activeElement?.dispatchEvent(new KeyboardEvent('keydown', {
    key: 'л', code: 'KeyK', bubbles: true, cancelable: true
  })));
  await expect(options).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(keyRequests).toBe(0);
  await page.keyboard.press('Escape');
  await newKey.press('k');
  await expect(page.getByRole('dialog')).toBeVisible();
  expect(keyRequests).toBe(1);
});

test('keeps an uncertain key issuance notice with the affected client', async ({ page }) => {
  const key: Key = { id: '1', prefix: 'fixture_key', createdAt: initialStart, lastUsedAt: null, revokedAt: null };
  await mockAPI(page, {
    initialClients: [
      { id: 'open-code', name: 'OpenCode', createdAt: initialStart, keys: [key] },
      { id: 'other', name: 'Other client', createdAt: initialStart, keys: [{ ...key, id: '2' }] }
    ]
  });
  await page.route('**/api/clients/open-code/keys', (route) => route.fulfill({ status: 503 }));
  await unlock(page);
  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('button', { name: 'Manage client OpenCode' }).click();
  await page.getByRole('button', { name: 'New key', exact: true }).press('Enter');
  const error = page.getByRole('alert').filter({ hasText: 'Could not issue a proxy key' });
  await expect(error).toContainText('Check the key list before issuing another.');
  await page.getByRole('button', { name: 'Clients', exact: true }).click();
  await page.getByRole('button', { name: 'Manage client Other client' }).click();
  await expect(page.getByRole('heading', { name: 'Other client' })).toBeVisible();
  await expect(error).toHaveCount(0);
  await page.getByRole('button', { name: /Revoke key/ }).press('Delete');
  const confirm = page.getByRole('alertdialog', { name: 'Revoke this key?' });
  await confirm.getByRole('button', { name: 'Cancel' }).press('Enter');
  await expect(confirm).toHaveCount(0);
  await page.getByRole('button', { name: 'Clients', exact: true }).click();
  await page.getByRole('button', { name: 'Manage client OpenCode' }).click();
  await expect(error).toBeVisible();
});

test('leaves a stalled key dialog and reloads the keys before another write', async ({ page }) => {
  const client: Client = { id: 'open-code', name: 'OpenCode', createdAt: initialStart, keys: [] };
  await mockAPI(page, { initialClients: [client] });
  await unlock(page);
  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('button', { name: 'Manage client OpenCode' }).click();
  const newKey = page.getByRole('button', { name: 'New key', exact: true });
  await expect(newKey).toBeEnabled();

  const refresh = Promise.withResolvers<void>();
  const refreshedKey: Key = { id: '1', prefix: 'possibly_issued', createdAt: initialStart, lastUsedAt: null, revokedAt: null };
  let writes = 0;
  await page.route('**/api/clients', async (route) => {
    if (!writes) return route.fallback();
    await refresh.promise;
    return route.fulfill({ json: { clients: [{ ...client, keys: [refreshedKey] }] } });
  });
  await page.route('**/api/clients/open-code/keys', () => { writes++; });
  await page.evaluate(() => {
    const timeout = AbortSignal.timeout;
    AbortSignal.timeout = () => {
      AbortSignal.timeout = timeout;
      const request = new AbortController();
      window.addEventListener('test-request-timeout', () => {
        request.abort(new DOMException('The operation timed out.', 'TimeoutError'));
      }, { once: true });
      return request.signal;
    };
  });

  await newKey.press('k');
  const pending = page.getByRole('dialog', { name: 'Generating proxy key' });
  await expect(pending).toBeVisible();
  await expect.poll(() => writes).toBe(1);
  await page.evaluate(() => window.dispatchEvent(new Event('test-request-timeout')));
  await expect(pending).toBeHidden();
  await expect(newKey).toBeDisabled();
  await expect(page.getByRole('alert')).toContainText('The proxy took too long to respond');
  await newKey.press('k');
  expect(writes).toBe(1);
  refresh.resolve();
  await expect(page.getByRole('button', { name: 'Revoke key possibly_issued' })).toBeVisible();
  await expect(newKey).toBeEnabled();
});

test('reconciles uncertain client creation and revocation and retains the draft', async ({ page }) => {
  const key: Key = { id: '1', prefix: 'fixture_key', createdAt: initialStart, lastUsedAt: null, revokedAt: null };
  const client: Client = { id: 'open-code', name: 'OpenCode', createdAt: initialStart, keys: [key] };
  let created = false;
  await mockAPI(page, { empty: true });
  await page.route('**/api/clients', async (route) => {
    if (route.request().method() === 'POST') {
      created = true;
      return route.fulfill({ status: 503 });
    }
    if (created) return route.fulfill({ json: { clients: [client] } });
    return route.fallback();
  });
  await page.route('**/api/clients/open-code/keys/1', (route) => {
    key.revokedAt = initialStart;
    return route.fulfill({ status: 503 });
  });

  await unlock(page);
  await page.getByRole('link', { name: 'Clients', exact: true }).press('Enter');
  const create = page.getByRole('button', { name: 'New client' });
  await expect(create).toBeEnabled();
  await create.press('n');
  const draft = page.getByRole('textbox', { name: 'Client name' });
  await draft.fill(client.name);
  await draft.press('Enter');
  await expect(page.getByRole('alert')).toContainText('Check the refreshed client list');
  await expect(page.getByRole('button', { name: 'Create client' })).toBeHidden();
  const back = page.getByRole('button', { name: 'Back to clients' });
  await expect(back).toBeFocused();
  await back.press('Enter');
  await expect(create).toBeEnabled();
  await create.press('n');
  await expect(draft).toHaveValue(client.name);
  await draft.press('Escape');

  await page.getByRole('button', { name: 'Manage client OpenCode' }).press('Enter');
  await page.getByRole('button', { name: 'Revoke key fixture_key' }).press('Delete');
  const confirm = page.getByRole('alertdialog', { name: 'Revoke this key?' });
  await confirm.getByRole('button', { name: 'Confirm revocation' }).press('Enter');
  await expect(confirm).toBeHidden();
  await expect(page.getByRole('alert')).toContainText('Check the refreshed key status');
  await expect(page.getByText('Revoked', { exact: true })).toBeVisible();
});

test('keeps client actions in view and restores focus after keyboard navigation', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 568 });
  const name = 'x'.repeat(100);
  await mockAPI(page, { initialClients: [
    { id: 'first-client', name: 'Another client', createdAt: initialStart, keys: [] },
    { id: 'open-code', name, createdAt: initialStart, keys: [] }
  ] });
  await unlock(page);
  await page.getByRole('link', { name: 'Clients', exact: true }).press('Enter');
  const manage = page.getByRole('button', { name: 'Manage client ' + name });
  await expect(manage).toBeInViewport({ ratio: 1 });
  await manage.press('Enter');
  await expect(page.getByRole('heading', { name })).toBeFocused();
  await page.getByRole('button', { name: 'Clients', exact: true }).press('Enter');
  await expect(manage).toBeFocused();
  await manage.press('Enter');
  await expect(page.getByRole('heading', { name })).toBeFocused();
  await page.goBack();
  await expect(manage).toBeFocused();
});

test('restores filter scope and pagination after undo and supports keyboard navigation', async ({ page }) => {
  const { usageQueries } = await mockAPI(page);
  await unlock(page);
  await page.getByRole('radio', { name: '7 days' }).click();
  await page.getByRole('combobox', { name: 'Source' }).click();
  await page.getByRole('option', { name: 'API providers' }).click();
  await expect(page).toHaveURL(/#usage\?range=7d&source=api$/);
  await expect(page.getByRole('row').filter({ has: page.getByText('OpenCode Go', { exact: true }) })).toBeVisible();

  await page.reload();
  await expect(page.getByRole('radio', { name: '7 days' })).toBeChecked();
  await expect(page.getByRole('combobox', { name: 'Source' })).toHaveText('API providers');
  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('link', { name: 'Skip to main content' }).press('Enter');
  await expect(page.getByRole('main')).toBeFocused();
  await expect(page).toHaveURL(/#clients$/);
  await page.getByRole('link', { name: 'Usage', exact: true }).click();
  await expect(page).toHaveURL(/#usage\?range=7d&source=api$/);
  const next = page.getByRole('button', { name: 'Next requests' });
  await expect(next).toBeEnabled();
  await next.press('Enter');
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  await expect.poll(() => usageQueries.at(-1)?.get('cursor')).toBe('test-cursor');
  await page.getByRole('button', { name: 'Reset filters' }).press('Enter');
  await expect(page).toHaveURL(/#usage$/);
  await expect(page.getByRole('radio', { name: 'Last 24h' })).toBeChecked();
  const undo = page.getByRole('button', { name: 'Undo reset' });
  await expect(undo).toBeFocused();
  await undo.press('Enter');
  await expect(page).toHaveURL(/#usage\?range=7d&source=api$/);
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  await expect.poll(() => usageQueries.at(-1)?.get('cursor')).toBe('test-cursor');
  await page.goBack();
  await expect(page.getByRole('radio', { name: 'Last 24h' })).toBeChecked();
});

test('guides an empty dashboard to a client and protects the first key before copying', async ({ page }) => {
  await mockAPI(page, { empty: true });
  await unlock(page);
  await expect(page.getByRole('heading', { name: 'No clients connected' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Codex allowance', exact: true })).toHaveCount(0);
  await page.getByRole('link', { name: 'Connect a client' }).press('Enter');
  const create = page.getByRole('button', { name: 'New client' });
  await expect(create).toBeEnabled();
  await create.press('n');
  const name = page.getByRole('textbox', { name: 'Client name' });
  await name.fill('OpenCode');
  await name.press('Enter');
  const secret = page.getByRole('textbox', { name: 'New proxy key', exact: true });
  await expect(secret).toHaveValue('test_key_1_secret');
  await page.keyboard.press('Escape');
  await expect(secret).toHaveValue('test_key_1_secret');
  const reveal = page.getByRole('dialog', { name: 'New proxy key' });
  await expect(reveal.getByText('X-LLM-Meter-Key', { exact: true })).toBeVisible();
  await reveal.getByRole('button', { name: 'Close without saving' }).click();
  const discard = page.getByRole('alertdialog', { name: 'Close without saving the key?' });
  await expect(discard).toBeVisible();
  await discard.getByRole('button', { name: 'Cancel' }).press('Enter');
  await expect(secret).toHaveValue('test_key_1_secret');
  await page.goBack();
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(secret).toHaveValue('test_key_1_secret');
  const warning = page.waitForEvent('dialog');
  await page.evaluate(() => { setTimeout(() => window.location.reload(), 0); });
  const unload = await warning;
  expect(unload.type()).toBe('beforeunload');
  await unload.dismiss();
  await expect(secret).toHaveValue('test_key_1_secret');
  await reveal.getByRole('button', { name: 'I saved the key' }).press('Enter');
  await expect(secret).not.toBeVisible();
  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('button', { name: 'Manage client OpenCode' }).click();
  await expect(page.getByText('Waiting for first request', { exact: true })).toBeVisible();
  await expect(page.getByText('X-LLM-Meter-Key', { exact: true })).toBeVisible();
});

test('keeps a client draft through session expiry and clears it on lock', async ({ page }) => {
  const { expireSession } = await mockAPI(page);
  await unlock(page);
  await page.getByRole('link', { name: 'Clients' }).click();
  const create = page.getByRole('button', { name: 'New client' });
  await expect(create).toBeEnabled();
  await create.press('n');
  const name = page.getByRole('textbox', { name: 'Client name' });
  await name.fill('Unsaved client');

  expireSession();
  await name.press('Enter');
  const token = page.getByLabel('Access token');
  await token.fill('dashboard-token');
  await token.press('Enter');
  await expect(create).toBeEnabled();
  await create.press('n');
  await expect(name).toHaveValue('Unsaved client');

  await name.press('Escape');
  await page.getByRole('button', { name: 'Lock dashboard' }).click();
  await token.fill('dashboard-token');
  await token.press('Enter');
  await expect(create).toBeEnabled();
  await create.press('n');
  await expect(name).toHaveValue('');
});

test.describe('mobile daily ranges west of UTC', () => {
  test.use({ timezoneId: 'America/Los_Angeles' });
  test('opens an attributed request from the UTC time picker and keeps date labels spaced', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await mockAPI(page);
    await unlock(page);
    for (const [range, count] of [['7 days', 8], ['30 days', 31]] as const) {
      await page.getByRole('radio', { name: range }).click();
      for (const [metric, name] of [['Usage value', 'Usage value over time'], ['Tokens', 'Tokens over time']] as const) {
        await page.getByRole('radiogroup', { name: 'Chart metric' }).getByRole('radio', { name: metric }).click();
        const chart = page.getByRole('region', { name });
        await expect(chart.getByRole('button')).toHaveCount(count);
        await expect(chart.getByRole('button').first()).toHaveAccessibleName(/^Nov 20 UTC/);
        await expect(chart.locator('.chart-labels span:visible').filter({ hasText: /\S/ })).toHaveCount(4);
      }
    }
    await page.getByRole('combobox', { name: 'Request time window' }).click();
    await page.getByRole('option', { name: 'Nov 20 UTC', exact: true }).click();
    await expect(page).toHaveURL(/bucket=2026-11-20/);
    await page.getByRole('button', { name: 'View requests' }).press('Enter');
    await expect(page.getByRole('heading', { name: 'Requests' })).toBeFocused();
    const request = page.getByRole('row', { name: /gpt-api.*OpenCode Go/ });
    await expect(request).toBeVisible();
    await request.getByRole('button', { name: /Details for gpt-api/ }).press('Enter');
    await expect(page.getByRole('dialog', { name: 'Request details' })).toBeVisible();
  });
});

test.describe('coarse touch input', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 320, height: 640 } });

  test('keeps dashboard commands and chart windows large enough to tap', async ({ page }) => {
    await mockAPI(page);
    await unlock(page);
    const dateRange = page.getByRole('radio', { name: '30 days' });
    await dateRange.tap();
    const chart = page.getByRole('region', { name: 'Tokens over time' });
    const window = chart.getByRole('button').nth(10);

    for (const target of [
      page.getByRole('button', { name: 'Lock dashboard' }),
      page.getByRole('button', { name: 'Refresh usage' }),
      dateRange,
      window
    ]) {
      const box = (await target.boundingBox())!;
      expect(box.width).toBeGreaterThanOrEqual(44);
      expect(box.height).toBeGreaterThanOrEqual(44);
    }

    await window.tap();
    await expect(window).toHaveAttribute('aria-pressed', 'true');
    await expect(page).toHaveURL(/bucket=2026-11-30/);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(320);
  });
});

for (const [width, height] of [[1280, 900], [320, 568]]) {
  test(`centers a compact New client dialog at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height });
    await mockAPI(page);
    await unlock(page);
    await page.getByRole('link', { name: 'Clients' }).click();
    await page.getByRole('button', { name: 'New client' }).click();
    const dialog = page.getByRole('dialog', { name: 'New client' });
    await dialog.getByRole('textbox', { name: 'Client name' }).fill('OpenCode');
    await expect(dialog).toBeInViewport({ ratio: 1 });
    const bounds = (await dialog.boundingBox())!;
    expect(bounds.height).toBeLessThan(300);
    expect(bounds.y + bounds.height / 2).toBeCloseTo(height / 2, 0);
    expect(bounds.x + bounds.width / 2).toBeCloseTo(width / 2, 0);
  });
}

test('creates a client and rotates its key using the dashboard', async ({ context, page }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const { usageQueries } = await mockAPI(page);
  await unlock(page);
  await page.getByRole('link', { name: 'Clients' }).click();
  const add = page.getByRole('button', { name: 'New client' });
  await expect(add).toBeEnabled();
  await expect(add).toHaveAttribute('aria-keyshortcuts', 'N');
  const name = page.getByRole('textbox', { name: 'Client name' });
  await page.getByRole('link', { name: 'Clients', exact: true }).press('n');
  await expect(name).toBeHidden();
  await add.press('n');
  await expect(name).toBeFocused();
  await name.fill('OpenCode');
  await page.keyboard.press('Escape');
  await expect(name).not.toBeVisible();
  await expect(add).toBeFocused();
  await add.dispatchEvent('keydown', { key: 'т', code: 'KeyN', isComposing: true });
  await expect(name).not.toBeVisible();
  await add.dispatchEvent('keydown', { key: 'т', code: 'KeyN', repeat: true });
  await expect(name).not.toBeVisible();
  await add.dispatchEvent('keydown', { key: 'т', code: 'KeyN' });
  await expect(name).toHaveValue('OpenCode');
  await name.press('Enter');
  const issuedKey = page.getByRole('textbox', { name: 'New proxy key', exact: true });
  await expect(issuedKey).toHaveValue('test_key_1_secret');
  const copy = page.getByRole('button', { name: 'Copy key' });
  await expect(copy).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByText('Key copied.', { exact: true })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('test_key_1_secret');
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(issuedKey).not.toBeVisible();
  const newKey = page.getByRole('button', { name: 'New key', exact: true });
  await expect(newKey).toHaveAttribute('aria-keyshortcuts', 'K');
  await page.getByRole('button', { name: 'Clients', exact: true }).press('k');
  await expect(issuedKey).toBeHidden();
  await newKey.press('k');
  await expect(issuedKey).toHaveValue('test_key_2_secret');
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
  });
  await copy.click();
  await expect(page.getByText('Key copied.', { exact: true })).toBeVisible();
  await expect(copy).toBeFocused();
  expect(await page.evaluate(() => {
    Reflect.deleteProperty(navigator, 'clipboard');
    return navigator.clipboard.readText();
  })).toBe('test_key_2_secret');
  await page.keyboard.press('Escape');
  const revoke = page.getByRole('button', { name: 'Revoke key test_key_1' });
  const revokeDialog = page.getByRole('alertdialog', { name: 'Revoke this key?' });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await revoke.evaluate((button) => button.getBoundingClientRect().right)).toBeLessThanOrEqual(390);
  await expect(revoke).toHaveAttribute('aria-keyshortcuts', 'Delete');
  await revoke.press('Delete');
  await expect(revokeDialog).toBeVisible();
  await revokeDialog.getByRole('button', { name: 'Cancel' }).press('Enter');
  await expect(revokeDialog).not.toBeVisible();
  await expect(revoke).toBeVisible();
  await revoke.press('Delete');
  await revokeDialog.getByRole('button', { name: 'Confirm revocation' }).press('Enter');
  await expect(page.getByText('Revoked', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'View usage' }).click();
  await expect.poll(() => usageQueries.at(-1)?.get('client')).toBe('open-code');
});

test('renames and archives a client by keyboard, preserving the draft and guarding writes', async ({ page }) => {
  const key: Key = { id: '1', prefix: 'fixture_key', createdAt: initialStart, lastUsedAt: null, revokedAt: null };
  await mockAPI(page, { initialClients: [{ id: 'open-code', name: 'OpenCode', createdAt: initialStart, keys: [key] }] });
  let saves = 0;
  let archives = 0;
  await page.route('**/api/clients/open-code', (route) => {
    if (route.request().method() === 'PATCH' && ++saves === 1) return route.fulfill({ status: 503 });
    if (route.request().method() === 'DELETE' && ++archives === 1) return route.fulfill({ status: 503 });
    return route.fallback();
  });
  await unlock(page);
  await page.getByRole('link', { name: 'Clients' }).press('Enter');
  await page.getByRole('button', { name: 'Manage client OpenCode' }).press('Enter');
  const rename = page.getByRole('button', { name: 'Rename' });
  const archive = page.getByRole('button', { name: 'Archive', exact: true });
  await expect(rename).toBeEnabled();
  await expect(rename).toHaveAttribute('aria-keyshortcuts', 'F2');
  await rename.focus();
  await rename.dispatchEvent('keydown', { key: 'F2', code: 'F2', repeat: true });
  await expect(page.getByRole('dialog')).toBeHidden();
  await rename.press('F2');
  const dialog = page.getByRole('dialog', { name: 'Rename client' });
  const name = dialog.getByRole('textbox', { name: 'Client name' });
  await expect(name).toBeFocused();
  await expect(name).toHaveValue('OpenCode');
  await name.fill(' ');
  const save = dialog.getByRole('button', { name: 'Save name' });
  await expect(save).toBeDisabled();
  await name.fill('  Рабочая станция  ');
  await name.press('Escape');
  await expect(rename).toBeFocused();
  await rename.press('F2');
  await expect(name).toHaveValue('  Рабочая станция  ');
  expect(await name.evaluate((input) => input.dispatchEvent(new KeyboardEvent('keydown', {
    key: 'Enter', code: 'Enter', isComposing: true, bubbles: true, cancelable: true
  })))).toBe(false);
  await expect(dialog).toBeVisible();
  expect(saves).toBe(0);
  await name.press('Enter');
  await expect(dialog.getByRole('alert')).toBeVisible();
  await expect(name).toHaveValue('  Рабочая станция  ');
  await name.press('Enter');
  await expect(dialog).toBeHidden();
  await expect(page.getByRole('heading', { name: 'Рабочая станция' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Revoke key fixture_key' })).toBeVisible();

  await expect(archive).toHaveAttribute('aria-keyshortcuts', 'Delete');
  await archive.press('Delete');
  const confirmation = page.getByRole('alertdialog', { name: 'Archive this client?' });
  const cancel = confirmation.getByRole('button', { name: 'Cancel' });
  await expect(cancel).toBeFocused();
  await cancel.press('Enter');
  await expect(archive).toBeFocused();
  await archive.press('Delete');
  const confirmArchive = confirmation.getByRole('button', { name: 'Confirm archive' });
  await confirmArchive.press('Enter');
  await expect(confirmation.getByRole('alert')).toContainText('Could not archive the client');
  await expect(confirmArchive).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#clients$/);
  await expect(page.getByRole('heading', { name: 'Clients', exact: true })).toBeFocused();
  await expect(page.getByText('No clients yet.', { exact: false })).toBeVisible();
});

test('the UI SDK exposes and unlocks the dashboard by accessible names', async ({ page }) => {
  await mockAPI(page);
  await page.goto('/');
  await expect(page).toHaveTitle('LLM Meter');
  await expect(page.getByRole('heading', { name: 'LLM Meter' })).toBeVisible();
  await expect(page.getByLabel('Access token')).toBeVisible();
  const result = await page.evaluate(async (path) => {
    const { agent } = await import(path);
    const labels = agent.snapshot().filter((entry: { offPage: boolean }) => !entry.offPage)
      .map((entry: { label: string }) => entry.label);
    const { ok, error } = await agent.act({
      steps: [
        { ref: 'Access token', action: 'setValue', args: ['dashboard-token'] },
        { ref: 'Unlock dashboard', action: 'press' }
      ]
    });
    return { labels, ok, error };
  }, '/test/agent.ts');
  expect(result.labels).toContain('Access token');
  expect(result.labels).toContain('Unlock dashboard');
  expect(result.ok, result.error?.message).toBe(true);
  await expect(page.getByRole('link', { name: 'LLM Meter' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Usage overview' })).toBeVisible();
});

test('the UI SDK exposes client management and gates archive confirmation', async ({ page }) => {
  await mockAPI(page, { initialClients: [{ id: 'open-code', name: 'OpenCode', createdAt: initialStart, keys: [] }] });
  await unlock(page);
  await page.getByRole('link', { name: 'Clients' }).click();
  await page.getByRole('button', { name: 'Manage client OpenCode' }).click();
  await expect(page.getByRole('button', { name: 'Rename' })).toBeEnabled();
  const result = await page.evaluate(async (path) => {
    const { agent } = await import(path);
    const rename = await agent.act({ steps: [
      { ref: 'Rename', action: 'press' },
      { ref: 'Client name', action: 'setValue', args: ['Workstation'] },
      { ref: 'Save name', action: 'press' }
    ] });
    const archive = await agent.act({ steps: [
      { ref: 'Archive', action: 'press' },
      { ref: 'Confirm archive', action: 'press' }
    ] });
    return { rename, archive };
  }, '/test/agent.ts');
  expect(result.rename.ok, result.rename.error?.message).toBe(true);
  expect(result.archive.ok).toBe(false);
  expect(result.archive.error?.message).toContain('Needs a person');
  await expect(page.getByRole('alertdialog', { name: 'Archive this client?' })).toContainText('Workstation');
});

test('bounds stalled unlock and lock requests and keeps keyboard recovery available', async ({ page }) => {
  await mockAPI(page);
  let unlocks = 0;
  let locks = 0;
  await page.route('**/api/login', (route) => {
    if (++unlocks > 1) return route.fallback();
  });
  await page.route('**/api/logout', (route) => {
    if (++locks > 1) return route.fallback();
  });
  const holdDeadline = () => page.evaluate(() => {
    const timeout = AbortSignal.timeout;
    AbortSignal.timeout = () => {
      AbortSignal.timeout = timeout;
      const request = new AbortController();
      window.addEventListener('test-auth-timeout', () => {
        request.abort(new DOMException('The operation timed out.', 'TimeoutError'));
      }, { once: true });
      return request.signal;
    };
  });
  const expireDeadline = () => page.evaluate(() => window.dispatchEvent(new Event('test-auth-timeout')));

  await page.goto('/');
  const token = page.getByLabel('Access token');
  await token.fill('dashboard-token');
  await holdDeadline();
  await token.press('Enter');
  await expect.poll(() => unlocks).toBe(1);
  await expect(token).toBeFocused();
  await expect(token).toHaveJSProperty('readOnly', true);
  await expect(page.getByRole('button', { name: 'Unlocking…' })).toBeDisabled();
  await expireDeadline();
  const error = page.getByRole('alert');
  await expect(error).toContainText('The proxy took too long to respond');
  await expect(token).toHaveAccessibleDescription(/The proxy took too long to respond/);
  await expect(token).toBeFocused();
  await expect(token).toHaveValue('dashboard-token');
  await expect(token).toBeEditable();
  await token.press('Enter');
  const usage = page.getByRole('heading', { name: 'Usage overview' });
  await expect(usage).toBeVisible();
  await expect(page.getByRole('main')).toBeFocused();

  const lock = page.getByRole('button', { name: 'Lock dashboard' });
  await expect(lock).toHaveAttribute('aria-keyshortcuts', 'Enter');
  await holdDeadline();
  await lock.press('Enter');
  await expect.poll(() => locks).toBe(1);
  const locking = page.getByRole('button', { name: 'Locking…' });
  await expect(locking).toBeDisabled();
  await expect(locking).toBeFocused();
  await expireDeadline();
  await expect(error).toContainText('The dashboard may still be unlocked');
  await expect(usage).toBeVisible();
  await expect(lock).toBeFocused();
  await lock.press('Enter');
  await expect(token).toBeFocused();
  await expect(token).toHaveValue('');
});

test('cancels a pending lock when the authenticated session expires', async ({ page }) => {
  const { expireSession } = await mockAPI(page);
  await unlock(page);
  const refresh = page.getByRole('button', { name: 'Refresh usage' });
  await expect(refresh).toBeEnabled();
  let lockSent = false;
  await page.route('**/api/logout', () => { lockSent = true; });
  const lockAborted = page.waitForEvent('requestfailed', {
    predicate: (request) => new URL(request.url()).pathname === '/api/logout',
    timeout: 5_000
  });
  await page.getByRole('button', { name: 'Lock dashboard' }).press('Enter');
  await expect.poll(() => lockSent).toBe(true);
  expireSession();
  await refresh.press('Enter');
  await lockAborted;
  const token = page.getByLabel('Access token');
  await expect(token).toBeFocused();
  await token.fill('dashboard-token');
  await token.press('Enter');
  await expect(page.getByRole('heading', { name: 'Usage overview' })).toBeVisible();
  await expect(page.getByRole('alert')).toBeHidden();
});

test('holds client writes while a pending lock could close the dashboard', async ({ page }) => {
  await mockAPI(page, { initialClients: [{ id: 'open-code', name: 'OpenCode', createdAt: initialStart, keys: [] }] });
  let failLock: (() => Promise<void>) | undefined;
  await page.route('**/api/logout', route => {
    failLock = () => route.fulfill({ status: 503 });
  });
  let keyWrites = 0;
  await page.route('**/api/clients/open-code/keys', route => {
    keyWrites++;
    return route.fallback();
  });
  await unlock(page);
  await page.getByRole('link', { name: 'Clients', exact: true }).press('Enter');
  await page.getByRole('button', { name: 'Manage client OpenCode' }).press('Enter');
  const newKey = page.getByRole('button', { name: 'New key' });
  await expect(newKey).toBeEnabled();
  await page.getByRole('button', { name: 'Lock dashboard' }).press('Enter');
  await expect.poll(() => Boolean(failLock)).toBe(true);
  await expect(newKey).toBeDisabled();
  await newKey.press('k');
  await newKey.press('Enter');
  expect(keyWrites).toBe(0);
  await expect(page.getByRole('dialog')).toBeHidden();

  await failLock!();
  await expect(newKey).toBeEnabled();
  await newKey.press('k');
  await expect(page.getByRole('dialog', { name: 'New proxy key' })).toBeVisible();
  expect(keyWrites).toBe(1);
});
