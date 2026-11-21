<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import ChartNoAxesCombinedIcon from '@lucide/svelte/icons/chart-no-axes-combined';
  import KeyRoundIcon from '@lucide/svelte/icons/key-round';
  import Layers3Icon from '@lucide/svelte/icons/layers-3';
  import LockKeyholeIcon from '@lucide/svelte/icons/lock-keyhole';
  import LogOutIcon from '@lucide/svelte/icons/log-out';
  import { Button } from '@ui-sdk/svelte/button';
  import { Field, FieldLabel } from '@ui-sdk/svelte/field';
  import { Input } from '@ui-sdk/svelte/input';
  import { APIError, api, dashboardProblem, jsonPost, type DashboardProblem, type RequestOutcome, type RequestPage, type Usage } from '../lib/api';
  import { guardEnter } from '../lib/format';
  import UsageView from '../lib/UsageView.svelte';
  import ClientsView from '../lib/ClientsView.svelte';
  import RecoveryNotice from '../lib/RecoveryNotice.svelte';

  const defaultFilters = { range: '24h', source: '', client: '', model: '', bucket: '', outcome: '' as RequestOutcome };
  type Filter = keyof typeof defaultFilters;
  type Screen = 'usage' | 'clients';
  type UsageScope = 'overview' | 'requests';
  type ScopedProblem = { query: string; problem: DashboardProblem };

  let session = $state<'checking' | 'locked' | 'ready' | 'unreachable'>('checking');
  let screen = $state<Screen>('usage');
  let clientId = $state('');
  let clientDraft = $state('');
  let keyDialogOpen = $state(false);
  let token = $state('');
  let loggingIn = $state(false);
  let loggingOut = $state(false);
  let authError = $state<DashboardProblem | null>(null);
  let tokenRejected = $state(false);
  let expiredSession = $state(false);
  let online = $state(true);

  let filters = $state({ ...defaultFilters });
  let cursors = $state<string[]>(['']);
  let overviewResult = $state<{ data: Usage; query: string; updatedAt: string } | null>(null);
  let requestResult = $state<{ data: RequestPage; query: string } | null>(null);
  let loadingScope = $state<UsageScope | null>(null);
  const overviewFailures = new SvelteMap<string, DashboardProblem>();
  let requestFailure = $state<ScopedProblem | null>(null);
  let request: AbortController | null = null;
  let refreshOnReturn = true;
  const usageHref = $derived(usageHash(filters));
  const overviewQuery = $derived(new URLSearchParams(
    Object.entries(filters).filter(([name, value]) => name !== 'bucket' && name !== 'outcome' && value)
  ).toString());
  const usageQuery = $derived.by(() => {
    const query = new URLSearchParams(overviewQuery);
    if (filters.bucket) query.set('bucket', filters.bucket);
    if (filters.outcome) query.set('outcome', filters.outcome);
    const cursor = cursors[cursors.length - 1];
    if (cursor) query.set('cursor', cursor);
    return query.toString();
  });
  const usageOverview = $derived(overviewResult?.query === overviewQuery ? overviewResult : null);
  const requestPage = $derived(requestResult?.query === usageQuery ? requestResult.data : null);
  const overviewError = $derived(overviewFailures.get(overviewQuery) ?? null);
  const requestError = $derived(requestFailure?.query === usageQuery ? requestFailure.problem : null);

  onMount(() => {
    online = navigator.onLine;
    readHash();
    void checkSession();
    const timer = window.setInterval(() => {
      if (canRefreshUsage() && cursors.length === 1) {
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
    const outcome = params.get('outcome');
    const next: typeof defaultFilters = {
      range: range === '7d' || range === '30d' ? range : defaultFilters.range,
      source: source === 'api' || source === 'codex' ? source : defaultFilters.source,
      client: filterText(params.get('client'), 100),
      model: filterText(params.get('model'), 500),
      bucket: params.get('bucket') ?? '',
      outcome: outcome === 'succeeded' || outcome === 'failed' ? outcome : ''
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

  function viewUsage(id: string, outcome: RequestOutcome = '') {
    navigateUsage({ ...filters, range: outcome ? '30d' : filters.range, client: id, source: '', model: '', bucket: '', outcome });
  }

  function filtersChanged(name: Filter, value: string) {
    if (filters[name] === value) return;
    navigateUsage({ ...filters, [name]: value, bucket: name === 'bucket' ? value : name === 'outcome' ? filters.bucket : '' });
  }

  function navigateUsage(values: typeof defaultFilters) {
    cursors = [''];
    window.location.hash = usageHash(values);
    readHash();
  }

  function refreshUsage() {
    void fetchUsage(overviewQuery, usageQuery, true);
  }

  function retryRequests() {
    void fetchUsage(overviewQuery, usageQuery, false);
  }

  function canRefreshUsage() {
    return online && session === 'ready' && screen === 'usage' && !keyDialogOpen && !document.hidden && !loadingScope;
  }

  function connectionChanged() {
    online = navigator.onLine;
    if (canRefreshUsage()) refreshUsage();
    else if (online && session === 'unreachable') void checkSession();
  }

  function lockSession(expired = false) {
    request?.abort();
    session = 'locked';
    expiredSession = expired;
    authError = null;
    token = '';
    tokenRejected = false;
    overviewResult = null;
    requestResult = null;
    overviewFailures.clear();
    requestFailure = null;
    cursors = [''];
  }

  async function checkSession() {
    session = 'checking';
    authError = null;
    try {
      await api('/session', { signal: AbortSignal.timeout(15_000) });
      session = 'ready';
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 401) session = 'locked';
      else {
        session = 'unreachable';
        authError = dashboardProblem(cause, 'Could not reach the dashboard');
      }
    }
  }

  async function login(event: SubmitEvent) {
    event.preventDefault();
    if (!token || loggingIn) return;
    loggingIn = true;
    authError = null;
    tokenRejected = false;
    try {
      await api('/login', jsonPost({ token }));
      token = '';
      expiredSession = false;
      session = 'ready';
    } catch (cause) {
      tokenRejected = cause instanceof APIError && cause.status === 401;
      authError = tokenRejected
        ? { title: 'That token was not accepted.', detail: 'Use the dashboard token configured for this proxy.' }
        : dashboardProblem(cause, 'Could not unlock the dashboard');
    } finally {
      loggingIn = false;
    }
  }

  async function logout() {
    if (loggingOut) return;
    loggingOut = true;
    authError = null;
    try {
      await api('/logout', { method: 'POST' });
    } catch (cause) {
      if (!(cause instanceof APIError && cause.status === 401)) {
        authError = dashboardProblem(cause, 'Could not lock the dashboard');
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
    const scope = loadOverview ? 'overview' : 'requests';
    refreshOnReturn = false;
    request?.abort();
    const current = new AbortController();
    request = current;
    loadingScope = scope;
    const signal = AbortSignal.any([current.signal, AbortSignal.timeout(15_000)]);
    try {
      if (loadOverview) {
        const data = await api<Usage>('/usage?' + query, { signal });
        if (current.signal.aborted) return;
        overviewResult = { data, query: overview, updatedAt: new Date().toISOString() };
        requestResult = { data, query };
        overviewFailures.delete(overview);
      } else {
        const data = await api<RequestPage>('/usage/requests?' + query, { signal });
        if (current.signal.aborted) return;
        requestResult = { data, query };
      }
      requestFailure = null;
    } catch (cause) {
      if (current.signal.aborted) return;
      if (cause instanceof APIError && cause.status === 401) {
        lockSession(true);
      } else if (loadOverview) {
        overviewFailures.set(overview, dashboardProblem(cause, 'Could not load usage'));
      } else {
        requestFailure = { query, problem: dashboardProblem(cause, 'Could not load requests') };
      }
    } finally {
      if (request === current) {
        loadingScope = null;
        request = null;
      }
    }
  }
</script>

<svelte:head>
  <title>LLM Proxy</title>
  <meta name="description" content="OpenAI API and Codex usage dashboard." />
</svelte:head>

<svelte:window onhashchange={readHash} ononline={connectionChanged} onoffline={connectionChanged} />

{#if session !== 'ready'}
  <main class="auth-page">
    <section class="auth-form-shell" aria-labelledby="auth-title">
      <div class="app-mark auth-mark"><Layers3Icon size={24} aria-hidden="true" /></div>
      <h1 id="auth-title">LLM Proxy</h1>
      {#if session === 'locked'}
        {#if expiredSession}<p class="muted" role="status">Your session expired. Unlock the dashboard to continue where you left off.</p>{/if}
        <form class="dialog-form auth-form" onsubmit={login}>
          <Field class="form-field" name="token">
            <FieldLabel>Access token</FieldLabel>
            <Input class="text-input" type="password" data-agent-secret required bind:value={token} autocomplete="current-password" placeholder="Enter your dashboard token" disabled={loggingIn} aria-invalid={tokenRejected} onkeydown={guardEnter} />
          </Field>
          {#if authError}<RecoveryNotice problem={authError} />{/if}
          <Button type="submit" class="primary-button unlock-button" aria-keyshortcuts="Enter" disabled={loggingIn || !token} onkeydown={guardEnter}>
            <LockKeyholeIcon size={15} aria-hidden="true" />
            {loggingIn ? 'Unlocking…' : 'Unlock dashboard'}
            <kbd aria-hidden="true" class="unlock-arrow">↵</kbd>
          </Button>
        </form>
      {:else if session === 'unreachable'}
        {#if authError}<RecoveryNotice problem={authError} retry={checkSession} />{/if}
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
      {#if authError}<RecoveryNotice problem={authError} />{/if}
      {#if screen === 'usage'}
        <UsageView
          data={usageOverview?.data ?? null}
          filterOptions={overviewResult?.data.filters}
          {requestPage}
          {loadingScope}
          {online}
          {overviewError}
          {requestError}
          updatedAt={usageOverview?.updatedAt ?? ''}
          {...filters}
          canPrevious={cursors.length > 1}
          pageNumber={cursors.length}
          update={filtersChanged}
          clearFilters={() => navigateUsage(defaultFilters)}
          refresh={refreshUsage}
          {retryRequests}
          onPrevious={() => cursors = cursors.slice(0, -1)}
          onNext={() => { if (requestPage?.nextCursor) cursors = [...cursors, requestPage.nextCursor]; }}
        />
      {/if}
      {#if screen === 'clients' || keyDialogOpen}
        <ClientsView active={screen === 'clients'} {clientId} bind:draft={clientDraft} bind:keyDialogOpen navigate={navigateClient} {viewUsage} onUnauthorized={() => lockSession(true)} />
      {/if}
    </main>
  </div>
{/if}
