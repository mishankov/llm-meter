<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import ChartNoAxesCombinedIcon from '@lucide/svelte/icons/chart-no-axes-combined';
  import KeyRoundIcon from '@lucide/svelte/icons/key-round';
  import Layers3Icon from '@lucide/svelte/icons/layers-3';
  import LockKeyholeIcon from '@lucide/svelte/icons/lock-keyhole';
  import LogOutIcon from '@lucide/svelte/icons/log-out';
  import { Button } from '@ui-sdk/svelte/button';
  import { Field, FieldLabel } from '@ui-sdk/svelte/field';
  import { Input } from '@ui-sdk/svelte/input';
  import { APIError, api, jsonPost, type RequestPage, type Usage } from '../lib/api';
  import { guardEnter } from '../lib/format';
  import UsageView from '../lib/UsageView.svelte';
  import ClientsView from '../lib/ClientsView.svelte';

  const defaultFilters = { range: '24h', source: '', client: '', model: '', bucket: '' };
  type Filter = keyof typeof defaultFilters;
  type Screen = 'usage' | 'clients';

  let session = $state<'checking' | 'locked' | 'ready' | 'unreachable'>('checking');
  let screen = $state<Screen>('usage');
  let clientId = $state('');
  let clientDraft = $state('');
  let keyDialogOpen = $state(false);
  let token = $state('');
  let loggingIn = $state(false);
  let loggingOut = $state(false);
  let authError = $state('');

  let filters = $state({ ...defaultFilters });
  let cursors = $state<string[]>(['']);
  let overviewResult = $state<{ data: Usage; query: string; updatedAt: string } | null>(null);
  let requestResult = $state<{ data: RequestPage; query: string } | null>(null);
  let loadingUsage = $state(false);
  let usageError = $state('');
  let request: AbortController | null = null;
  let refreshOnReturn = true;
  const usageHref = $derived(usageHash(filters));
  const overviewQuery = $derived(new URLSearchParams(
    Object.entries(filters).filter(([name, value]) => name !== 'bucket' && value)
  ).toString());
  const usageQuery = $derived.by(() => {
    const query = new URLSearchParams(overviewQuery);
    if (filters.bucket) query.set('bucket', filters.bucket);
    const cursor = cursors[cursors.length - 1];
    if (cursor) query.set('cursor', cursor);
    return query.toString();
  });
  const usageOverview = $derived(overviewResult?.query === overviewQuery ? overviewResult : null);
  const requestPage = $derived(requestResult?.query === usageQuery ? requestResult.data : null);

  onMount(() => {
    readHash();
    void checkSession();
    const timer = window.setInterval(() => {
      if (session === 'ready' && screen === 'usage' && !keyDialogOpen && !document.hidden && cursors.length === 1 && !loadingUsage) {
        refreshUsage();
      }
    }, 12_000);
    return () => {
      window.clearInterval(timer);
      request?.abort();
    };
  });

  $effect(() => {
    if (session !== 'ready' || screen !== 'usage' || keyDialogOpen) {
      request?.abort();
      refreshOnReturn = true;
      return;
    }
    const overview = overviewQuery;
    const query = usageQuery;
    untrack(() => void fetchUsage(overview, query, refreshOnReturn));
  });

  function usageHash(values: typeof defaultFilters) {
    const query = new URLSearchParams();
    for (const name of Object.keys(defaultFilters) as Filter[]) {
      if (values[name] !== defaultFilters[name]) query.set(name, values[name]);
    }
    return query.size ? '#usage?' + query.toString() : '#usage';
  }

  function readFilters(query: string) {
    const params = new URLSearchParams(query);
    const range = params.get('range');
    const source = params.get('source');
    const next = {
      range: range === '7d' || range === '30d' ? range : defaultFilters.range,
      source: source === 'api' || source === 'codex' ? source : defaultFilters.source,
      client: filterText(params.get('client'), 100),
      model: filterText(params.get('model'), 500),
      bucket: params.get('bucket') ?? ''
    };
    const interval = (next.range === '24h' ? 1 : 24) * 3_600_000;
    if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(next.bucket) ||
      Date.parse(next.bucket) % interval !== 0) next.bucket = '';
    if (usageHash(next) !== usageHash(filters)) {
      filters = next;
      cursors = [''];
    }
  }

  function filterText(value: string | null, maxBytes: number) {
    return value && new TextEncoder().encode(value).length <= maxBytes ? value : '';
  }

  function readHash() {
    const [path, query = ''] = window.location.hash.slice(1).split('?');
    const [section, selected] = path.split('/');
    screen = section === 'clients' ? 'clients' : 'usage';
    clientId = screen === 'clients' && selected ? selected : '';
    if (screen === 'usage') readFilters(query);
  }

  function navigateClient(id?: string) {
    const route = id ? '#clients/' + encodeURIComponent(id) : '#clients';
    window.location.hash = route;
    readHash();
  }

  function viewUsage(id: string) {
    navigateUsage({ ...filters, client: id, source: '', model: '', bucket: '' });
  }

  function filtersChanged(name: Filter, value: string) {
    if (filters[name] === value) return;
    navigateUsage({ ...filters, [name]: value, bucket: name === 'bucket' ? value : '' });
  }

  function navigateUsage(values: typeof defaultFilters) {
    cursors = [''];
    window.location.hash = usageHash(values);
    readHash();
  }

  function refreshUsage() {
    void fetchUsage(overviewQuery, usageQuery, true);
  }

  function lockSession() {
    request?.abort();
    session = 'locked';
    token = '';
    overviewResult = null;
    requestResult = null;
    cursors = [''];
  }

  async function checkSession() {
    session = 'checking';
    try {
      await api('/session');
      session = 'ready';
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 401) session = 'locked';
      else session = 'unreachable';
    }
  }

  async function login(event: SubmitEvent) {
    event.preventDefault();
    if (!token || loggingIn) return;
    loggingIn = true;
    authError = '';
    try {
      await api('/login', jsonPost({ token }));
      token = '';
      session = 'ready';
    } catch (cause) {
      authError = cause instanceof APIError && cause.status === 401
        ? 'That token was not accepted.'
        : 'Could not unlock the dashboard. Try again.';
    } finally {
      loggingIn = false;
    }
  }

  async function logout() {
    if (loggingOut) return;
    loggingOut = true;
    authError = '';
    try {
      await api('/logout', { method: 'POST' });
    } catch (cause) {
      if (!(cause instanceof APIError && cause.status === 401)) {
        authError = 'Could not lock the dashboard. Try again.';
        return;
      }
    } finally {
      loggingOut = false;
    }
    clientDraft = '';
    lockSession();
  }

  async function fetchUsage(overview: string, query: string, refreshOverview: boolean) {
    const loadOverview = refreshOverview || overviewResult?.query !== overview;
    refreshOnReturn = false;
    request?.abort();
    const current = new AbortController();
    request = current;
    loadingUsage = true;
    usageError = '';
    try {
      if (loadOverview) {
        const data = await api<Usage>('/usage?' + query, { signal: current.signal });
        if (current.signal.aborted) return;
        overviewResult = { data, query: overview, updatedAt: new Date().toISOString() };
        requestResult = { data, query };
      } else {
        const data = await api<RequestPage>('/usage/requests?' + query, { signal: current.signal });
        if (current.signal.aborted) return;
        requestResult = { data, query };
      }
    } catch (cause) {
      if (current.signal.aborted) return;
      if (cause instanceof APIError && cause.status === 401) lockSession();
      else usageError = loadOverview ? 'Could not load usage.' : 'Could not load requests.';
    } finally {
      if (request === current) {
        loadingUsage = false;
        request = null;
      }
    }
  }
</script>

<svelte:head>
  <title>LLM Proxy</title>
  <meta name="description" content="OpenAI API and Codex usage dashboard." />
</svelte:head>

<svelte:window onhashchange={readHash} />

{#if session !== 'ready'}
  <main class="auth-page">
    <section class="auth-form-shell" aria-labelledby="auth-title">
      <div class="app-mark auth-mark"><Layers3Icon size={24} aria-hidden="true" /></div>
      <h1 id="auth-title">LLM Proxy</h1>
      {#if session === 'locked'}
        <form class="dialog-form auth-form" onsubmit={login}>
          <Field class="form-field" name="token">
            <FieldLabel>Access token</FieldLabel>
            <Input class="text-input" type="password" data-agent-secret required bind:value={token} autocomplete="current-password" placeholder="Enter your dashboard token" disabled={loggingIn} aria-invalid={Boolean(authError)} onkeydown={guardEnter} />
          </Field>
          {#if authError}<p class="field-error" role="alert">{authError}</p>{/if}
          <Button type="submit" class="primary-button unlock-button" aria-keyshortcuts="Enter" disabled={loggingIn || !token} onkeydown={guardEnter}>
            <LockKeyholeIcon size={15} aria-hidden="true" />
            {loggingIn ? 'Unlocking…' : 'Unlock dashboard'}
            <kbd aria-hidden="true" class="unlock-arrow">↵</kbd>
          </Button>
        </form>
      {:else if session === 'unreachable'}
        <p class="field-error" role="alert">Could not reach the dashboard.</p>
        <Button class="primary-button" onclick={checkSession}>Retry</Button>
      {:else}
        <p class="muted" role="status">Checking session…</p>
      {/if}
    </section>
  </main>
{:else}
  <div class="shell">
    <aside class="sidebar">
      <a class="brand" href={usageHref}>
        <span class="app-mark"><Layers3Icon size={19} aria-hidden="true" /></span>
        <span>LLM Proxy</span>
      </a>
      <nav aria-label="Main navigation">
        <a href={usageHref} aria-current={screen === 'usage' ? 'page' : undefined}>
          <ChartNoAxesCombinedIcon size={17} aria-hidden="true" /> Usage
        </a>
        <a href="#clients" aria-current={screen === 'clients' ? 'page' : undefined}>
          <KeyRoundIcon size={17} aria-hidden="true" /> Clients
        </a>
      </nav>
      <div class="nav-end">
        <Button class="lock-button" onclick={logout} disabled={loggingOut} title="Lock dashboard">
          <LogOutIcon size={16} aria-hidden="true" /> <span>{loggingOut ? 'Locking…' : 'Lock dashboard'}</span>
        </Button>
      </div>
    </aside>

    <main id="content" class="content">
      {#if authError}<div class="feedback error-feedback" role="alert">{authError}</div>{/if}
      {#if screen === 'usage'}
        <UsageView
          data={usageOverview?.data ?? null}
          filterOptions={overviewResult?.data.filters}
          {requestPage}
          loading={loadingUsage}
          error={usageError}
          updatedAt={usageOverview?.updatedAt ?? ''}
          {...filters}
          canPrevious={cursors.length > 1}
          update={filtersChanged}
          clearFilters={() => navigateUsage(defaultFilters)}
          refresh={refreshUsage}
          onPrevious={() => cursors = cursors.slice(0, -1)}
          onNext={() => { if (requestPage?.nextCursor) cursors = [...cursors, requestPage.nextCursor]; }}
        />
      {/if}
      {#if screen === 'clients' || keyDialogOpen}
        <ClientsView active={screen === 'clients'} {clientId} bind:draft={clientDraft} bind:keyDialogOpen navigate={navigateClient} {viewUsage} onUnauthorized={lockSession} />
      {/if}
    </main>
  </div>
{/if}
