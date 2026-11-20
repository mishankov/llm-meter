import { expect, test, type Page } from '@playwright/test';
import type { Client, Key, Usage } from '../src/lib/api';

const initialStart = '2026-11-20T12:00:00Z';
const rates = { input: 1, cachedInput: .1, output: 2, cacheWrite: null };
const response: Usage = {
  summary: { apiCost: 0, codexValue: .016, inputTokens: 360, cachedInputTokens: 60, outputTokens: 40, requestCount: 2 },
  buckets: [
    { start: initialStart, apiCost: 0, codexValue: 0, inputTokens: 300, cachedInputTokens: 60, outputTokens: 30 },
    { start: '2026-11-20T13:00:00Z', apiCost: 0, codexValue: .016, inputTokens: 60, cachedInputTokens: 0, outputTokens: 10 }
  ],
  groups: {
    models: [
      { id: 'gpt-api', name: 'gpt-api', apiCost: 0, codexValue: 0, tokens: 330 },
      { id: 'gpt-codex', name: 'gpt-codex', apiCost: 0, codexValue: .016, tokens: 70 }
    ],
    clients: [{ id: 'open-code', name: 'OpenCode', apiCost: 0, codexValue: 0, tokens: 330 }]
  },
  requests: [
    { id: 1, at: '2026-11-20T12:12:00Z', source: 'api', provider: 'opencode-go', clientId: 'open-code', client: 'OpenCode', model: 'gpt-api', endpoint: '/opencode-go/responses', method: 'POST', status: 200, usageAvailable: true, inputTokens: 300, cachedInputTokens: 60, outputTokens: 30, cost: 0, rates: null, priceSource: 'provider-reported', reportedListCost: .045, reportedAllowance: .018 },
    { id: 2, at: '2026-11-20T13:02:00Z', source: 'codex', clientId: '', client: '', model: 'gpt-codex', endpoint: '/backend-api/codex/responses', method: 'WS', status: 200, usageAvailable: true, inputTokens: 60, cachedInputTokens: 0, outputTokens: 10, cost: .016, rates }
  ],
  allowance: [{ id: 'open-code-window', label: '5-hour window', remainingPercent: 77, resetsAt: '2026-11-20T16:00:00Z' }],
  filters: { models: ['gpt-api', 'gpt-codex'], clients: [{ id: 'open-code', name: 'OpenCode' }] },
  nextCursor: null
};

async function mockAPI(page: Page) {
  let authorized = false;
  let keyCount = 0;
  const clients: Client[] = [];
  const usageQueries: URLSearchParams[] = [];
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
    if (url.pathname === '/api/usage') {
      usageQueries.push(url.searchParams);
      const cursor = url.searchParams.get('cursor');
      const bucket = url.searchParams.get('bucket');
      const client = url.searchParams.get('client');
      const source = url.searchParams.get('source');
      const days = url.searchParams.get('range') === '30d' ? 31 : url.searchParams.get('range') === '7d' ? 8 : 0;
      const buckets = days ? Array.from({ length: days }, (_, index) => ({
        ...response.buckets[0],
        start: new Date(Date.parse(initialStart) + index * 86_400_000).toISOString()
      })) : response.buckets;
      const requests = response.requests.filter((request) =>
        (!bucket || request.at.slice(0, 13) === bucket.slice(0, 13)) &&
        (!client || request.clientId === client) &&
        (!source || request.source === source) &&
        (!cursor || request.source === 'codex')
      );
      const nextCursor = !cursor && !bucket && !client ? 'test-cursor' : null;
      return route.fulfill({ json: { ...response, buckets, requests, nextCursor } });
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
  return { usageQueries, expireSession: () => { authorized = false; } };
}

async function unlock(page: Page) {
  await page.goto('/');
  const field = page.getByLabel('Access token');
  await field.fill('dashboard-token');
  await field.press('Enter');
  await expect(page.getByRole('heading', { name: 'Usage overview' })).toBeVisible();
}

test('filters requests from a chart and inspects and pages through them with the keyboard', async ({ page }) => {
  const { usageQueries } = await mockAPI(page);
  await unlock(page);
  const chart = page.getByRole('region', { name: 'Cost over time' });
  await expect(chart).toBeVisible();
  const firstBucket = chart.getByRole('button').first();
  await firstBucket.focus();
  await page.keyboard.press('ArrowRight');
  await expect(chart.getByRole('button').nth(1)).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(firstBucket).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(firstBucket).toHaveAttribute('aria-pressed', 'true');
  await expect(firstBucket).toBeFocused();
  await expect.poll(() => usageQueries.at(-1)?.get('bucket')).toBe(initialStart);
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

test('hides previous results when loading the selected source fails', async ({ page }) => {
  await mockAPI(page);
  let failFilteredQuery = true;
  await page.route('**/api/usage?**', (route) => {
    const source = new URL(route.request().url()).searchParams.get('source');
    return source === 'codex' && failFilteredQuery ? route.fulfill({ status: 503 }) : route.fallback();
  });
  await unlock(page);
  const apiRequest = page.getByRole('button', { name: /Details for gpt-api/ });
  await expect(apiRequest).toBeVisible();
  await page.getByRole('combobox', { name: 'Source' }).click();
  await page.getByRole('option', { name: 'Codex subscription' }).click();
  await expect(page.getByRole('alert')).toHaveText('Could not load usage. Retry');
  await expect(apiRequest).not.toBeVisible();
  await expect(page.getByRole('region', { name: 'Cost over time' })).not.toBeVisible();

  failFilteredQuery = false;
  await page.getByRole('button', { name: 'Retry' }).click();
  await expect(page.getByRole('button', { name: /Details for gpt-codex/ })).toBeVisible();
  await expect(apiRequest).not.toBeVisible();
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

test('shows spaced date labels for both daily chart ranges on mobile', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockAPI(page);
  await unlock(page);
  for (const [range, count] of [['7 days', 8], ['30 days', 31]] as const) {
    await page.getByRole('radio', { name: range }).click();
    for (const name of ['Cost over time', 'Tokens over time']) {
      const chart = page.getByRole('region', { name });
      await expect(chart.getByRole('button')).toHaveCount(count);
      await expect(chart.locator('.chart-labels span:visible').filter({ hasText: /\S/ })).toHaveCount(4);
    }
  }
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
