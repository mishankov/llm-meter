import { expect, test, type Page } from '@playwright/test';
import type { Client, Key, Usage, UsageRequest } from '../src/lib/api';

const initialStart = '2026-11-20T12:00:00Z';
const rates = { input: 1, cachedInput: .1, output: 2, cacheWrite: null };
const response: Usage = {
  summary: { estimatedCost: .016, inputTokens: 360, cachedInputTokens: 60, outputTokens: 40, requestCount: 4, pricedRequestCount: 2 },
  buckets: [
    { start: initialStart, estimatedCost: 0, inputTokens: 300, cachedInputTokens: 60, outputTokens: 30, requestCount: 2, pricedRequestCount: 1 },
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
    { id: 1, at: '2026-11-20T12:12:00Z', source: 'api', provider: 'opencode-go', clientId: 'open-code', client: 'OpenCode', model: 'gpt-api', endpoint: '/opencode-go/responses', method: 'POST', status: 200, usageAvailable: true, inputTokens: 300, cachedInputTokens: 60, outputTokens: 30, cost: 0, rates: null, priceSource: 'provider-reported', reportedListCost: .045, reportedAllowance: .018 },
    { id: 2, at: '2026-11-20T13:02:00Z', source: 'codex', clientId: '', client: '', model: 'gpt-codex', endpoint: '/backend-api/codex/responses', method: 'WS', status: 200, usageAvailable: true, inputTokens: 60, cachedInputTokens: 0, outputTokens: 10, cost: .016, rates },
    { id: 3, at: '2026-11-20T14:03:00Z', source: 'api', clientId: 'open-code', client: 'OpenCode', model: 'gpt-unpriced', endpoint: '/v1/responses', method: 'POST', status: 200, usageAvailable: true, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, cost: null, rates: null },
    { id: 4, at: '2026-11-20T12:13:00Z', source: 'api', clientId: 'open-code', client: 'OpenCode', model: 'gpt-unpriced', endpoint: '/v1/responses', method: 'POST', status: 200, usageAvailable: true, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, cost: null, rates: null }
  ],
  allowance: [{ id: 'open-code-window', label: '5-hour window', remainingPercent: 77, resetsAt: '2026-11-20T16:00:00Z' }],
  filters: { models: ['gpt-api', 'gpt-codex'], clients: [{ id: 'open-code', name: 'OpenCode' }] },
  nextCursor: null
};

async function mockAPI(page: Page, {
  empty = false, initialClients = [], extraRequests = []
}: { empty?: boolean; initialClients?: Client[]; extraRequests?: UsageRequest[] } = {}) {
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
        buckets: [], groups: { models: [], clients: [] }, requests: [], allowance: [],
        filters: { models: [], clients: clients.map(({ id, name }) => ({ id, name })) }
      } : {
        ...response,
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
  await expect(requests.getByText('Unpriced', { exact: true })).toHaveCount(2);
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
  await expect(firstBucket.locator('.chart-tooltip')).toContainText('1 of 2 priced');
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
  await mockAPI(page);
  await unlock(page);
  await page.getByRole('button', { name: /Details for gpt-api/ }).waitFor();

  for (const width of [300, 320, 390, 768]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
    await expect(page.getByRole('button', { name: 'Lock dashboard' })).toBeInViewport({ ratio: 1 });
  }
});

test('starts with one chart and makes both desktop trends available through Compare', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockAPI(page);
  await unlock(page);
  const metrics = page.getByRole('radiogroup', { name: 'Chart metric' });
  const valueChart = page.getByRole('region', { name: 'Usage value over time' });
  const tokenChart = page.getByRole('region', { name: 'Tokens over time' });
  await expect(metrics.getByRole('radio', { name: 'Usage value' })).toBeChecked();
  await expect(valueChart).toBeVisible();
  await expect(tokenChart).toHaveCount(0);
  await metrics.getByRole('radio', { name: 'Usage value' }).press('ArrowRight');
  await expect(metrics.getByRole('radio', { name: 'Tokens' })).toBeChecked();
  await expect(tokenChart).toBeVisible();
  await expect(valueChart).toHaveCount(0);
  await metrics.getByRole('radio', { name: 'Tokens' }).press('ArrowRight');
  await expect(metrics.getByRole('radio', { name: 'Compare' })).toBeChecked();
  await expect(valueChart).toBeVisible();
  await expect(tokenChart).toBeVisible();
  await page.getByRole('link', { name: 'Clients', exact: true }).click();
  await page.getByRole('link', { name: 'Usage', exact: true }).click();
  await expect(metrics.getByRole('radio', { name: 'Compare' })).toBeChecked();
  await expect(valueChart).toBeVisible();
  await expect(tokenChart).toBeVisible();
  const requests = page.getByRole('region', { name: 'Requests', exact: true });
  await expect(requests.getByText('4 requests on this page')).toBeVisible();
});

test('shows one estimated USD bar per model and switches units with the keyboard', async ({ page }) => {
  await mockAPI(page);
  await unlock(page);
  const breakdown = page.getByRole('region', { name: 'Usage breakdown' });
  const highestEstimate = breakdown.getByRole('button', { name: /gpt-codex/ });
  const fills = highestEstimate.locator('.rank-track > span');
  const usd = breakdown.getByRole('radio', { name: 'Usage value' });
  const tokens = breakdown.getByRole('radio', { name: 'Tokens' });

  await expect(breakdown.getByRole('button').first()).toHaveAccessibleName(/gpt-codex/);
  await expect(highestEstimate.getByText('$0.02', { exact: true })).toBeVisible();
  await expect(breakdown.getByRole('button', { name: /gpt-unpriced/ }).getByText('Unpriced', { exact: true })).toBeVisible();
  await expect(fills).toHaveCount(1);
  await expect(fills).toHaveAttribute('style', /width: 100%/);
  await usd.press('ArrowRight');
  await expect(tokens).toBeChecked();
  await expect(highestEstimate.getByText('70', { exact: true })).toBeVisible();
  await tokens.press('ArrowLeft');
  await expect(usd).toBeChecked();
  await expect(highestEstimate.getByText('$0.02', { exact: true })).toBeVisible();
  await breakdown.getByRole('radio', { name: 'Client' }).click();
  const mixed = breakdown.getByRole('button', { name: /OpenCode/ });
  await expect(mixed.getByText('$0.00', { exact: true })).toBeVisible();
  await expect(mixed.getByText('1 of 3 priced', { exact: true })).toBeVisible();
});

test('aligns usage bars with differently sized amounts and keeps rows keyboard-operable', async ({ page }) => {
  const { usageQueries } = await mockAPI(page);
  await unlock(page);
  const breakdown = page.getByRole('region', { name: 'Usage breakdown' });
  const tracks = breakdown.locator('.rank-track');
  await expect(tracks).toHaveCount(4);

  for (const width of [1280, 320]) {
    await page.setViewportSize({ width, height: 900 });
    const columns = await tracks.evaluateAll((elements) => elements.map((element) => {
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
  await expect(page.getByRole('status')).toHaveText('Loading usage…');
  await expect(apiRequest).not.toBeVisible();
  finishLoading();
  await expect(page.getByRole('alert')).toContainText('Could not load usage');
  await expect(page.getByRole('alert')).toContainText('Check the LLM Proxy service and its logs.');
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
  const baseURL = new URL('/backend-api/codex', page.url()).href;
  await expect(page.getByRole('textbox', { name: 'Base URL' })).toHaveValue(baseURL);
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  const copyURL = page.getByRole('button', { name: 'Copy URL', exact: true });
  await expect(copyURL).toHaveAttribute('aria-keyshortcuts', 'Enter');
  await copyURL.press('Enter');
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(baseURL);
  await page.getByRole('button', { name: 'Provider setup instructions' }).press('Enter');
  await expect(page.getByText('config.toml', { exact: true })).toBeVisible();
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

test('restores filter scope and pagination after undo and supports keyboard navigation', async ({ page }) => {
  const { usageQueries } = await mockAPI(page);
  await unlock(page);
  await page.getByRole('radio', { name: '7 days' }).click();
  await page.getByRole('combobox', { name: 'Source' }).click();
  await page.getByRole('option', { name: 'API providers' }).click();
  await expect(page).toHaveURL(/#usage\?range=7d&source=api$/);
  await expect(page.getByRole('row', { name: /OpenCode Go/ })).toBeVisible();

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
  await page.getByRole('link', { name: 'Connect a client' }).press('Enter');
  const create = page.getByRole('button', { name: 'New client' });
  await expect(create).toBeEnabled();
  await page.keyboard.press('n');
  const name = page.getByRole('textbox', { name: 'Client name' });
  await name.fill('OpenCode');
  await name.press('Enter');
  const secret = page.getByRole('textbox', { name: 'New proxy key', exact: true });
  await expect(secret).toHaveValue('test_key_1_secret');
  await page.keyboard.press('Escape');
  await expect(secret).toHaveValue('test_key_1_secret');
  const reveal = page.getByRole('dialog', { name: 'New proxy key' });
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
  await expect(page.getByText('X-LLM-Proxy-Key', { exact: true })).toBeVisible();
});

test('keeps a client draft through session expiry and clears it on lock', async ({ page }) => {
  const { expireSession } = await mockAPI(page);
  await unlock(page);
  await page.getByRole('link', { name: 'Clients' }).click();
  await expect(page.getByRole('button', { name: 'New client' })).toBeEnabled();
  await page.keyboard.press('n');
  const name = page.getByRole('textbox', { name: 'Client name' });
  await name.fill('Unsaved client');

  expireSession();
  await name.press('Enter');
  const token = page.getByLabel('Access token');
  await token.fill('dashboard-token');
  await token.press('Enter');
  await expect(page.getByRole('button', { name: 'New client' })).toBeEnabled();
  await page.keyboard.press('n');
  await expect(name).toHaveValue('Unsaved client');

  await name.press('Escape');
  await page.getByRole('button', { name: 'Lock dashboard' }).click();
  await token.fill('dashboard-token');
  await token.press('Enter');
  await expect(page.getByRole('button', { name: 'New client' })).toBeEnabled();
  await page.keyboard.press('n');
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
  await page.keyboard.press('n');
  const name = page.getByRole('textbox', { name: 'Client name' });
  await expect(name).toBeFocused();
  await name.fill('OpenCode');
  await page.keyboard.press('Escape');
  await expect(name).not.toBeVisible();
  await page.locator('body').dispatchEvent('keydown', { key: 'т', code: 'KeyN', isComposing: true, bubbles: true });
  await expect(name).not.toBeVisible();
  await page.locator('body').dispatchEvent('keydown', { key: 'т', code: 'KeyN', bubbles: true });
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
  await page.keyboard.press('k');
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

test('the UI SDK exposes and unlocks the dashboard by accessible names', async ({ page }) => {
  await mockAPI(page);
  await page.goto('/');
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
  await expect(page.getByRole('heading', { name: 'Usage overview' })).toBeVisible();
});
