<script lang="ts">
  import { afterNavigate, goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount, tick, untrack } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import ChartNoAxesCombinedIcon from '@lucide/svelte/icons/chart-no-axes-combined';
  import KeyRoundIcon from '@lucide/svelte/icons/key-round';
  import LockKeyholeIcon from '@lucide/svelte/icons/lock-keyhole';
  import LogOutIcon from '@lucide/svelte/icons/log-out';
  import { Button } from '@ui-sdk/svelte/button';
  import { Field, FieldLabel } from '@ui-sdk/svelte/field';
  import { Input } from '@ui-sdk/svelte/input';
  import { APIError, api, dashboardProblem, jsonPost, type DashboardProblem, type RequestOutcome, type RequestPage, type Usage } from '../../lib/api';
  import favicon from '../../lib/assets/favicon.png?no-inline';
  import meterMark from '../../lib/assets/llm-meter.png';
  import { guardEnter } from '../../lib/format';
  import RecoveryNotice from '../../lib/RecoveryNotice.svelte';
  import { defaultFilters, overviewParams, parseUsageFilters, requestParams, usageURL, type UsageFilters } from '../../lib/usage-filters';

  type Screen = 'usage' | 'clients';
  type UsageScope = 'overview' | 'requests';
  type ScopedProblem = { query: string; problem: DashboardProblem };
  const responseTimeout = 15_000;

  let session = $state<'checking' | 'locked' | 'ready' | 'unreachable'>('checking');
  let screen = $state<Screen>('usage');
  let clientId = $state('');
  let clientDraft = $state('');
  const renameDrafts = new SvelteMap<string, string>();
  let keyDialogOpen = $state(false);
  let token = $state('');
  let loggingIn = $state(false);
  let loggingOut = $state(false);
  let logoutRequest: AbortController | null = null;
  let authError = $state<DashboardProblem | null>(null);
  let tokenRejected = $state(false);
  let expiredSession = $state(false);
  let online = $state(true);

  let filters = $state({ ...defaultFilters });
  let pendingUsage: UsageFilters | null = null;
  let chartMetric = $state<'cost' | 'tokens'>('tokens');
  let cursors = $state<string[]>(['']);
  let resetSnapshot = $state<{ filters: UsageFilters; cursors: string[] } | null>(null);
  let requestFocus = $state(false);
  let overviewResult = $state<{ data: Usage; query: string; updatedAt: string } | null>(null);
  let requestResult = $state<{ data: RequestPage; query: string } | null>(null);
  let loadingScope = $state<UsageScope | null>(null);
  const overviewFailures = new SvelteMap<string, DashboardProblem>();
  let requestFailure = $state<ScopedProblem | null>(null);
  let request: AbortController | null = null;
  let refreshOnReturn = true;
  const usageHref = $derived(usageURL(filters));
  const overviewQuery = $derived(overviewParams(filters).toString());
  const usageQuery = $derived.by(() => {
    const query = requestParams(filters);
    const cursor = cursors[cursors.length - 1];
    if (cursor) query.set('cursor', cursor);
    return query.toString();
  });
  const usageOverview = $derived(overviewResult?.query === overviewQuery ? overviewResult : null);
  const requestPage = $derived(requestResult?.query === usageQuery ? requestResult.data : null);
  const overviewError = $derived(overviewFailures.get(overviewQuery) ?? null);
  const requestError = $derived(requestFailure?.query === usageQuery ? requestFailure.problem : null);

  afterNavigate(({ to }) => {
    if (!to) return;
    const legacy = legacyURL(to.url);
    if (legacy) {
      void goto(legacy, { replace: true, reset: false });
      return;
    }
    requestFocus = false;
    screen = to.params?.section === 'clients' ? 'clients' : 'usage';
    clientId = screen === 'clients' ? to.url.searchParams.get('client') ?? '' : '';
    if (screen === 'usage') readFilters(to.url.searchParams);
  });

  onMount(() => {
    online = navigator.onLine;
    void checkSession();
    const timer = window.setInterval(() => {
      if (canRefreshUsage() && cursors.length === 1) {
        refreshUsage();
      }
    }, 12_000);
    return () => {
      window.clearInterval(timer);
      request?.abort();
      logoutRequest?.abort();
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

  function readFilters(params: URLSearchParams) {
    const next = parseUsageFilters(params);
    const nextURL = usageURL(next);
    if (nextURL !== usageHref) {
      if (nextURL !== '/usage') resetSnapshot = null;
      filters = next;
      cursors = [''];
    }
  }

  function legacyURL(url: URL) {
    if (url.pathname !== '/') return;
    const [path, query = ''] = url.hash.slice(1).split('?', 2);
    if (path === 'usage') return query ? '/usage?' + query : '/usage';
    if (path === 'clients') return '/clients';
    const selected = /^clients\/([^/]+)$/.exec(path)?.[1];
    if (selected) return '/clients?' + new URLSearchParams('client=' + selected);
  }

  function legacyHashChanged() {
    const target = legacyURL(new URL(window.location.href));
    // A browser-created fragment entry is outside SvelteKit's managed history.
    if (target) window.location.replace(target);
  }

  function navigateClient(id?: string) {
    const route = id ? '/clients?' + new URLSearchParams({ client: id }) : '/clients';
    return goto(route, { reset: false });
  }

  function viewUsage(id: string, outcome: RequestOutcome = '') {
    void navigateUsage({ ...filters, range: outcome ? '30d' : filters.range, client: id ? [id] : [], source: [], model: [], bucket: '', outcome }, outcome !== '');
  }

  function filtersChanged<Name extends keyof UsageFilters>(name: Name, value: UsageFilters[Name]) {
    const active = pendingUsage ?? filters;
    if (active[name] === value) return;
    const next = { ...active, [name]: value };
    if (name !== 'bucket' && name !== 'outcome') next.bucket = '';
    void navigateUsage(next);
  }

  async function navigateUsage(values: UsageFilters, focusRequests = false, cursorHistory = ['']) {
    const route = usageURL(values);
    const sameURL = page.url.pathname + page.url.search === route;
    const supersedesPending = pendingUsage !== null;
    pendingUsage = values;
    try {
      if (!sameURL || supersedesPending) await goto(route, { reset: false, replace: sameURL });
      if (pendingUsage === values && page.url.pathname + page.url.search === route) {
        cursors = cursorHistory;
        requestFocus = focusRequests;
      }
    } finally {
      if (pendingUsage === values) pendingUsage = null;
    }
  }

  function resetFilters() {
    if (usageHref === '/usage') return;
    resetSnapshot = { filters: { ...filters }, cursors: [...cursors] };
    void navigateUsage(defaultFilters);
  }

  function undoFilters() {
    const previous = resetSnapshot;
    if (!previous) return;
    resetSnapshot = null;
    void navigateUsage(previous.filters, false, previous.cursors);
  }

  function skipToContent(event: MouseEvent) {
    event.preventDefault();
    document.getElementById('content')?.focus();
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
    logoutRequest?.abort();
    logoutRequest = null;
    loggingOut = false;
    keyDialogOpen = false;
    session = 'locked';
    expiredSession = expired;
    authError = null;
    token = '';
    tokenRejected = false;
    overviewResult = null;
    requestResult = null;
    resetSnapshot = null;
    requestFocus = false;
    overviewFailures.clear();
    requestFailure = null;
    cursors = [''];
    void tick().then(() => document.getElementById('access-token')?.focus());
  }

  async function checkSession() {
    session = 'checking';
    authError = null;
    try {
      await api('/session', { signal: AbortSignal.timeout(responseTimeout) });
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
    if (session !== 'locked' || !token || loggingIn) return;
    loggingIn = true;
    authError = null;
    tokenRejected = false;
    try {
      await api('/login', { ...jsonPost({ token }), signal: AbortSignal.timeout(responseTimeout) });
      token = '';
      expiredSession = false;
      session = 'ready';
      await tick();
      document.getElementById('content')?.focus();
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
    if (session !== 'ready' || loggingOut || keyDialogOpen) return;
    const current = new AbortController();
    logoutRequest = current;
    loggingOut = true;
    authError = null;
    try {
      const signal = AbortSignal.any([current.signal, AbortSignal.timeout(responseTimeout)]);
      await api('/logout', { method: 'POST', signal });
    } catch (cause) {
      if (current.signal.aborted) return;
      if (!(cause instanceof APIError && cause.status === 401)) {
        const problem = dashboardProblem(cause, 'Could not lock the dashboard');
        authError = { ...problem, detail: 'The dashboard may still be unlocked. ' + problem.detail };
        return;
      }
    } finally {
      if (logoutRequest === current) {
        logoutRequest = null;
        loggingOut = false;
      }
    }
    if (current.signal.aborted) return;
    clientDraft = '';
    renameDrafts.clear();
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
    const signal = AbortSignal.any([current.signal, AbortSignal.timeout(responseTimeout)]);
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

{#snippet screenLoadError(name: string)}
  <RecoveryNotice
    problem={{ title: 'Could not load ' + name, detail: 'Reload the dashboard to try opening this screen again.' }}
    retry={() => window.location.reload()} retryLabel="Reload dashboard"
  />
{/snippet}

<svelte:head>
  <title>LLM Meter</title>
  <meta name="description" content="Dashboard for LLM usage, costs, and provider allowances." />
  <link rel="icon" type="image/png" sizes="32x32" href={favicon} />
</svelte:head>

<svelte:window onhashchange={legacyHashChanged} ononline={connectionChanged} onoffline={connectionChanged} />

{#if session !== 'ready'}
  <main class="auth-page">
    <section class="auth-form-shell" aria-labelledby="auth-title">
      <img class="app-mark auth-mark" src={meterMark} alt="" />
      <h1 id="auth-title">LLM Meter</h1>
      {#if session === 'locked'}
        {#if expiredSession}<p class="muted" role="status">Your session expired. Unlock the dashboard to continue where you left off.</p>{/if}
        <form class="dialog-form auth-form" onsubmit={login}>
          <Field class="form-field" name="token">
            <FieldLabel>Access token</FieldLabel>
            <Input id="access-token" class="text-input" type="password" data-agent-secret required bind:value={token} autocomplete="current-password" placeholder="Enter your dashboard token" readOnly={loggingIn} aria-invalid={tokenRejected} aria-describedby={authError ? 'unlock-error' : undefined} onkeydown={guardEnter} />
          </Field>
          {#if authError}<div id="unlock-error"><RecoveryNotice problem={authError} /></div>{/if}
          <Button type="submit" class="primary-button unlock-button" aria-keyshortcuts="Enter" disabled={loggingIn || !token} focusableWhenDisabled={loggingIn} onkeydown={guardEnter}>
            <LockKeyholeIcon size={15} aria-hidden="true" />
            {loggingIn ? 'Unlocking…' : 'Unlock dashboard'}
            <kbd aria-hidden="true" class="unlock-arrow">↵</kbd>
          </Button>
          <span class="sr-only" role="status">{loggingIn ? 'Unlocking dashboard…' : ''}</span>
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
    <a class="skip-link" href="#content" onclick={skipToContent} onkeydown={guardEnter}>Skip to main content</a>
    <aside class="sidebar">
      <a class="brand" href={usageHref}>
        <img class="app-mark" src={meterMark} alt="" />
        <span>LLM Meter</span>
      </a>
      <nav aria-label="Main navigation">
        <a href={usageHref} aria-current={screen === 'usage' ? 'page' : undefined}>
          <ChartNoAxesCombinedIcon size={17} aria-hidden="true" /> Usage
        </a>
        <a href="/clients" aria-current={screen === 'clients' ? 'page' : undefined}>
          <KeyRoundIcon size={17} aria-hidden="true" /> Clients
        </a>
      </nav>
      <div class="nav-end">
        <Button class="lock-button" title="Lock dashboard (Enter)" aria-keyshortcuts="Enter" disabled={loggingOut || keyDialogOpen} focusableWhenDisabled={loggingOut} onclick={logout} onkeydown={guardEnter}>
          <LogOutIcon size={16} aria-hidden="true" /> <span>{loggingOut ? 'Locking…' : 'Lock dashboard'}</span>
        </Button>
        <span class="sr-only" role="status">{loggingOut ? 'Locking dashboard…' : ''}</span>
      </div>
    </aside>

    <main id="content" class="content" tabindex="-1">
      {#if authError}<RecoveryNotice problem={authError} />{/if}
      {#if screen === 'usage'}
        {#await import('../../lib/UsageView.svelte')}
          <p class="muted" role="status">Opening usage…</p>
        {:then { default: UsageView }}
          <UsageView
            bind:chartMetric
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
            clearFilters={resetFilters}
            canUndoReset={resetSnapshot !== null}
            {undoFilters}
            focusRequests={requestFocus}
            onRequestFocus={() => { requestFocus = false; }}
            refresh={refreshUsage}
            {retryRequests}
            onPrevious={() => cursors = cursors.slice(0, -1)}
            onNext={() => { if (requestPage?.nextCursor) cursors = [...cursors, requestPage.nextCursor]; }}
          />
        {:catch}
          {@render screenLoadError('usage')}
        {/await}
      {/if}
      {#if screen === 'clients' || keyDialogOpen}
        {#await import('../../lib/ClientsView.svelte')}
          <p class="muted" role="status">Opening clients…</p>
        {:then { default: ClientsView }}
          <ClientsView active={screen === 'clients'} {clientId} {loggingOut} bind:draft={clientDraft} {renameDrafts} bind:keyDialogOpen navigate={navigateClient} {viewUsage} onUnauthorized={() => lockSession(true)} />
        {:catch}
          {@render screenLoadError('clients')}
        {/await}
      {/if}
    </main>
  </div>
{/if}
