<script lang="ts">
  import { onMount } from 'svelte';
  import ChartNoAxesCombinedIcon from '@lucide/svelte/icons/chart-no-axes-combined';
  import KeyRoundIcon from '@lucide/svelte/icons/key-round';
  import Layers3Icon from '@lucide/svelte/icons/layers-3';
  import LockKeyholeIcon from '@lucide/svelte/icons/lock-keyhole';
  import LogOutIcon from '@lucide/svelte/icons/log-out';
  import { Button } from '@ui-sdk/svelte/button';
  import { Field, FieldLabel } from '@ui-sdk/svelte/field';
  import { Input } from '@ui-sdk/svelte/input';
  import { APIError, api, jsonPost, type Usage } from '../lib/api';
  import { guardEnter } from '../lib/format';
  import UsageView from '../lib/UsageView.svelte';
  import ClientsView from '../lib/ClientsView.svelte';

  type Filter = 'range' | 'source' | 'client' | 'model' | 'bucket';
  type Screen = 'usage' | 'clients';

  let session = $state<'checking' | 'locked' | 'ready' | 'unreachable'>('checking');
  let screen = $state<Screen>('usage');
  let clientId = $state('');
  let clientDraft = $state('');
  let token = $state('');
  let loggingIn = $state(false);
  let loggingOut = $state(false);
  let authError = $state('');

  let filters = $state({ range: '24h', source: '', client: '', model: '', bucket: '' });
  let cursors = $state<string[]>(['']);
  let usage = $state<Usage | null>(null);
  let loadingUsage = $state(false);
  let usageError = $state('');
  let updatedAt = $state('');
  let request: AbortController | null = null;

  onMount(() => {
    readHash();
    void checkSession();
    const timer = window.setInterval(() => {
      if (session === 'ready' && screen === 'usage' && !document.hidden && cursors.length === 1 && !loadingUsage) {
        void fetchUsage();
      }
    }, 12_000);
    return () => {
      window.clearInterval(timer);
      request?.abort();
    };
  });

  $effect(() => {
    if (session === 'ready' && screen === 'usage') {
      void fetchUsage();
    } else {
      request?.abort();
    }
  });

  function readHash() {
    const [section, selected] = window.location.hash.slice(1).split('/');
    screen = section === 'clients' ? 'clients' : 'usage';
    clientId = screen === 'clients' && selected ? selected : '';
  }

  function navigateClient(id?: string) {
    const route = id ? '#clients/' + encodeURIComponent(id) : '#clients';
    window.location.hash = route;
    readHash();
  }

  function viewUsage(id: string) {
    filters = { ...filters, client: id, source: '', model: '', bucket: '' };
    cursors = [''];
    window.location.hash = '#usage';
    readHash();
  }

  function filtersChanged(name: Filter, value: string) {
    if (filters[name] === value) return;
    filters = { ...filters, [name]: value, bucket: name === 'bucket' ? value : '' };
    cursors = [''];
  }

  function lockSession() {
    request?.abort();
    session = 'locked';
    token = '';
    usage = null;
    clientDraft = '';
    updatedAt = '';
    filters = { range: '24h', source: '', client: '', model: '', bucket: '' };
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
      lockSession();
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 401) lockSession();
      else authError = 'Could not lock the dashboard. Try again.';
    } finally {
      loggingOut = false;
    }
  }

  async function fetchUsage() {
    const query = new URLSearchParams();
    for (const [name, value] of Object.entries(filters)) {
      if (value) query.set(name, value);
    }
    const cursor = cursors[cursors.length - 1];
    if (cursor) query.set('cursor', cursor);
    request?.abort();
    const current = new AbortController();
    request = current;
    loadingUsage = true;
    usageError = '';
    try {
      const result = await api<Usage>('/usage?' + query, { signal: current.signal });
      if (!current.signal.aborted) {
        usage = result;
        updatedAt = new Date().toISOString();
      }
    } catch (cause) {
      if (current.signal.aborted) return;
      if (cause instanceof APIError && cause.status === 401) lockSession();
      else usageError = 'Could not load usage.';
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
    <section class="panel auth-panel" aria-labelledby="auth-title">
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
      <a class="brand" href="#usage">
        <span class="app-mark"><Layers3Icon size={19} aria-hidden="true" /></span>
        <span>LLM Proxy</span>
      </a>
      <nav aria-label="Main navigation">
        <a href="#usage" aria-current={screen === 'usage' ? 'page' : undefined}>
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
          data={usage}
          loading={loadingUsage}
          error={usageError}
          {updatedAt}
          {...filters}
          canPrevious={cursors.length > 1}
          update={filtersChanged}
          refresh={() => void fetchUsage()}
          onPrevious={() => cursors = cursors.slice(0, -1)}
          onNext={() => { if (usage?.nextCursor) cursors = [...cursors, usage.nextCursor]; }}
        />
      {:else}
        <ClientsView {clientId} bind:draft={clientDraft} navigate={navigateClient} {viewUsage} onUnauthorized={lockSession} />
      {/if}
    </main>
  </div>
{/if}
