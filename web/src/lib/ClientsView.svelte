<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
  import ChartNoAxesCombinedIcon from '@lucide/svelte/icons/chart-no-axes-combined';
  import CheckIcon from '@lucide/svelte/icons/check';
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
  import CopyIcon from '@lucide/svelte/icons/copy';
  import KeyRoundIcon from '@lucide/svelte/icons/key-round';
  import PlusIcon from '@lucide/svelte/icons/plus';
  import XIcon from '@lucide/svelte/icons/x';
  import { AlertDialog, AlertDialogBackdrop, AlertDialogClose, AlertDialogContent, AlertDialogDescription, AlertDialogTitle, AlertDialogTrigger } from '@ui-sdk/svelte/alert-dialog';
  import { Button } from '@ui-sdk/svelte/button';
  import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/svelte/dialog';
  import { Field, FieldError, FieldLabel } from '@ui-sdk/svelte/field';
  import { Input } from '@ui-sdk/svelte/input';
  import { APIError, api, dashboardProblem, jsonPost, type Client, type Key, type NewKey, type RequestOutcome } from './api';
  import { dismissShortcut, formatDateTime, guardEnter, requestStatusGuidance, requestStatusLabel } from './format';
  import ClientSetup from './ClientSetup.svelte';
  import RecoveryNotice from './RecoveryNotice.svelte';

  type Problem = ReturnType<typeof dashboardProblem>;
  const responseTimeout = 15_000;

  interface Props {
    active: boolean;
    clientId: string;
    loggingOut: boolean;
    draft?: string;
    keyDialogOpen?: boolean;
    navigate: (clientId?: string) => void;
    viewUsage: (clientId: string, outcome?: RequestOutcome) => void;
    onUnauthorized: () => void;
  }
  let { active, clientId, loggingOut, draft = $bindable(''), keyDialogOpen = $bindable(false), navigate, viewUsage, onUnauthorized }: Props = $props();

  let clients = $state<Client[]>([]);
  let selected = $derived(clients.find((client) => client.id === clientId));
  let hasActiveKey = $derived(selected?.keys.some((key) => !key.revokedAt));
  let hasUsedKey = $derived(selected?.keys.some((key) => !key.revokedAt && key.lastUsedAt));
  let lastRequest = $derived(selected?.lastRequest);
  let lastRequestFailed = $derived(lastRequest && (lastRequest.status === 0 || lastRequest.status >= 400));
  let proxyOrigin = $state('');
  let loading = $state(true);
  let loadError = $state<Problem | null>(null);
  const actionFailures = new SvelteMap<string, Problem>();
  let actionError = $derived(actionFailures.get(clientId) ?? null);
  let createError = $state<{ problem: Problem; uncertain: boolean } | null>(null);
  let clientNameTooLong = $derived([...draft.trim()].length > 100);
  let busy = $state(false);
  let dialogOpen = $state(false);
  let dialogMode = $state<'create' | 'key'>('create');
  let revokeOpen = $state(false);
  let generated = $state<NewKey | null>(null);
  let revoking = $state<{ clientId: string; key: Key; trigger: HTMLElement } | null>(null);
  let revokeError = $derived(actionFailures.get(revoking?.clientId ?? '') ?? null);
  let copyState = $state<'idle' | 'copied' | 'manual'>('idle');
  let discardKey = false;
  let listRequest: AbortController | null = null;
  let returnFocus = $state<HTMLElement | null>(null);
  let previousClientId = '';

  $effect(() => {
    keyDialogOpen = (dialogOpen && (busy || generated !== null)) || (revokeOpen && busy);
  });

  $effect(() => {
    if (revokeOpen && !busy && (!active || revoking?.clientId !== clientId)) revokeOpen = false;
  });

  $effect(() => {
    const origin = previousClientId;
    previousClientId = clientId;
    if (active && !clientId && origin && !dialogOpen && !revokeOpen && document.activeElement === document.body) {
      const target = document.getElementById('manage-client-' + origin) ?? document.getElementById('clients-title');
      target?.focus();
    }
  });

  $effect(() => {
    if (!dialogOpen || (!busy && (!generated || copyState === 'copied'))) return;
    const warnBeforeLeaving = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = true;
    };
    window.addEventListener('beforeunload', warnBeforeLeaving);
    return () => window.removeEventListener('beforeunload', warnBeforeLeaving);
  });

  function problem(cause: unknown, title: string) {
    if (cause instanceof APIError && cause.status === 401) onUnauthorized();
    return dashboardProblem(cause, title);
  }

  function mutationFailure(cause: unknown, title: string, guidance: string) {
    const failure = problem(cause, title);
    const uncertain = !(cause instanceof APIError) || cause.status >= 500;
    return { problem: uncertain ? { ...failure, detail: guidance } : failure, uncertain };
  }

  async function load(silent = false) {
    if (silent && (loading || busy || dialogOpen || revokeOpen || listRequest)) return;
    listRequest?.abort();
    const current = new AbortController();
    listRequest = current;
    if (!silent) loading = true;
    try {
      const signal = AbortSignal.any([current.signal, AbortSignal.timeout(responseTimeout)]);
      const result = await api<{ clients: Client[] }>('/clients', { signal });
      if (current.signal.aborted) return;
      clients = result.clients;
      loadError = null;
    } catch (cause) {
      if (current.signal.aborted) return;
      const failure = problem(cause, 'Could not load clients');
      if (!loadError || failure.title !== loadError.title || failure.detail !== loadError.detail) loadError = failure;
    } finally {
      if (listRequest === current) {
        listRequest = null;
        if (!silent) loading = false;
      }
    }
  }

  onMount(() => {
    proxyOrigin = window.location.origin;
    void load();
    const timer = window.setInterval(() => { if (!document.hidden) void load(true); }, 5_000);
    return () => {
      window.clearInterval(timer);
      listRequest?.abort();
    };
  });

  function canManageClients() {
    return active && !loggingOut && !loading && !loadError && !busy;
  }

  function canCreate() {
    return canManageClients() && !dialogOpen && !revokeOpen;
  }

  function canSubmitClient() {
    return canManageClients() && !!draft.trim() && !clientNameTooLong && !createError?.uncertain;
  }

  function openCreate(event: MouseEvent) {
    if (!canCreate()) return;
    listRequest?.abort();
    returnFocus = event.currentTarget as HTMLElement;
    generated = null;
    createError = null;
    dialogMode = 'create';
    dialogOpen = true;
  }

  async function createClient(event: SubmitEvent) {
    event.preventDefault();
    if (!canSubmitClient()) return;
    listRequest?.abort();
    busy = true;
    createError = null;
    try {
      const result = await api<{ client: Client; key: NewKey }>('/clients', {
        ...jsonPost({ name: draft.trim() }),
        signal: AbortSignal.timeout(responseTimeout)
      });
      clients = [result.client, ...clients];
      draft = '';
      navigate(result.client.id);
      await reveal(result.key);
    } catch (cause) {
      createError = mutationFailure(cause, 'Could not create the client', 'Check the refreshed client list before trying again. Your client name will be kept.');
      if (createError.uncertain) void load();
    } finally {
      busy = false;
    }
    if (createError) {
      await tick();
      document.getElementById(createError.uncertain ? 'close-client-form' : 'client-name')?.focus();
    }
  }

  async function makeKey(event: MouseEvent) {
    if (!selected || !canCreate()) return;
    listRequest?.abort();
    const id = selected.id;
    returnFocus = event.currentTarget as HTMLElement;
    busy = true;
    actionFailures.delete(id);
    generated = null;
    dialogMode = 'key';
    dialogOpen = true;
    try {
      const result = await api<{ key: NewKey }>('/clients/' + encodeURIComponent(id) + '/keys', {
        method: 'POST',
        signal: AbortSignal.timeout(responseTimeout)
      });
      const { token, ...metadata } = result.key;
      clients = clients.map((client) => client.id === id ? { ...client, keys: [metadata, ...client.keys] } : client);
      await reveal({ ...metadata, token });
    } catch (cause) {
      const failure = mutationFailure(cause, 'Could not issue a proxy key', 'Check the key list before issuing another. Revoke any key whose full value was lost.');
      actionFailures.set(id, failure.problem);
      if (failure.uncertain) void load();
      dialogOpen = false;
    } finally {
      busy = false;
    }
  }

  async function reveal(key: NewKey) {
    generated = key;
    copyState = 'idle';
    dialogOpen = true;
    await tick();
    document.getElementById('copy-key')?.focus();
  }

  async function copyKey() {
    const key = generated;
    if (!key) return;
    try {
      await navigator.clipboard.writeText(key.token);
      if (generated === key) copyState = 'copied';
      return;
    } catch {
      // The Clipboard API may be unavailable on HTTP or denied by the browser.
    }

    if (generated !== key) return;
    const token = document.getElementById('issued-key');
    if (!(token instanceof HTMLInputElement)) return;

    const previousFocus = document.activeElement;
    token.focus();
    token.select();
    try {
      if (document.execCommand('copy')) {
        if (previousFocus instanceof HTMLElement) previousFocus.focus();
        copyState = 'copied';
        return;
      }
    } catch {
      // Leave the full key selected if the browser also blocks this copy command.
    }
    copyState = 'manual';
  }

  function confirmRevoke(key: Key, event: MouseEvent) {
    if (!canCreate()) return;
    listRequest?.abort();
    revoking = { clientId, key, trigger: event.currentTarget as HTMLElement };
    revokeOpen = true;
    actionFailures.delete(clientId);
  }

  function revokeShortcut(event: KeyboardEvent) {
    guardEnter(event);
    if (event.key === 'Delete' && !dismissShortcut(event)) {
      event.preventDefault();
      (event.currentTarget as HTMLButtonElement).click();
    }
  }

  async function revoke() {
    if (!revoking || revoking.clientId !== clientId || !canManageClients()) return;
    listRequest?.abort();
    const id = revoking.clientId;
    const keyId = revoking.key.id;
    actionFailures.delete(id);
    busy = true;
    try {
      await api('/clients/' + encodeURIComponent(id) + '/keys/' + encodeURIComponent(keyId), {
        method: 'DELETE',
        signal: AbortSignal.timeout(responseTimeout)
      });
      clients = clients.map((client) => client.id === id ? {
        ...client, keys: client.keys.map((key) => key.id === keyId ? { ...key, revokedAt: new Date().toISOString() } : key)
      } : client);
      revokeOpen = false;
      await tick();
      document.getElementById('new-key')?.focus();
    } catch (cause) {
      const failure = mutationFailure(cause, 'Could not revoke the key', 'Check the refreshed key status before trying again.');
      actionFailures.set(id, failure.problem);
      if (failure.uncertain) {
        void load();
        revokeOpen = false;
      }
    } finally {
      busy = false;
    }
  }

  function clientShortcut(event: KeyboardEvent, key: 'N' | 'K') {
    guardEnter(event);
    const button = event.currentTarget;
    if (!(button instanceof HTMLButtonElement) || button !== document.activeElement ||
      event.defaultPrevented || dismissShortcut(event) || !canCreate()) return;
    const matchesKey = event.key.toUpperCase() === key;
    const matchesLayout = /^\p{L}$/u.test(event.key) && !/^[a-z]$/i.test(event.key) && event.code === 'Key' + key;
    if (!matchesKey && !matchesLayout) return;
    event.preventDefault();
    button.click();
  }

  function activeKeys(client: Client) {
    return client.keys.filter((key) => key.revokedAt === null).length;
  }

  async function openClient(id: string, event: MouseEvent) {
    navigate(id);
    void load(true);
    if (event.detail !== 0) return;
    await tick();
    if (active && clientId === id && !dialogOpen && !revokeOpen) {
      document.getElementById('client-title')?.focus();
    }
  }

  function keyDialogReturnFocus() {
    if (returnFocus?.isConnected) return returnFocus;
    return document.getElementById('new-key') ?? document.querySelector<HTMLElement>('nav a[aria-current="page"]');
  }

  function revokeDialogReturnFocus() {
    if (!loading && revoking?.trigger.isConnected) return revoking.trigger;
    return document.getElementById('keys-title') ?? document.getElementById('clients-title');
  }
</script>

<svelte:window ononline={() => active && load(true)} />

{#if active && loadError}
  <RecoveryNotice problem={loadError} retry={() => load()} busy={loading} cached={clients.length > 0} retryLabel="Retry clients" />
{/if}

{#if active && clientId}
  <div class="client-crumb">
    <Button class="secondary-button back-button" onclick={() => navigate()}>
      <ArrowLeftIcon size={15} aria-hidden="true" /> Clients
    </Button>
  </div>
  {#if selected}
    <header class="page-head">
      <div><h1 id="client-title" tabindex="-1">{selected.name}</h1><p>Created {formatDateTime(selected.createdAt)}</p></div>
      <div class="head-actions">
        <Button class="secondary-button" onclick={() => viewUsage(selected?.id ?? '')}>
          <ChartNoAxesCombinedIcon size={15} aria-hidden="true" /> View usage
        </Button>
        <Button id="new-key" class="primary-button" aria-keyshortcuts="K" title="New key (K when focused)"
          disabled={!canCreate()} focusableWhenDisabled onclick={makeKey} onkeydown={(event) => clientShortcut(event, 'K')}>
          <PlusIcon size={16} aria-hidden="true" /> New key <kbd aria-hidden="true">K</kbd>
        </Button>
      </div>
    </header>
    {#if actionError && !revokeOpen}<RecoveryNotice problem={actionError} />{/if}
    <section class="connection" aria-labelledby="connection-title">
      <h2 id="connection-title">Connect this client</h2>
      <div class="connection-columns">
        {#key clientId}
          <ClientSetup origin={proxyOrigin} disabled={busy || dialogOpen || revokeOpen} />
        {/key}
        <div class="connection-instructions">
          <div class="connection-status" role="status">
            {#if !hasActiveKey}
              <strong>No active proxy key</strong>
              <span>Use New key to connect this client.</span>
            {:else if lastRequest}
              <strong>Latest recorded request</strong>
              <span>
                <b class="request-result" class:failed={lastRequestFailed}>
                  {requestStatusLabel(lastRequest.status)}{lastRequest.status ? ` (${lastRequest.status})` : ''}
                </b>
                <span aria-hidden="true"> · </span>
                <time datetime={lastRequest.at}>{formatDateTime(lastRequest.at)}</time>
              </span>
              {#if lastRequestFailed}
                <span>{requestStatusGuidance(lastRequest.status)}</span>
              {:else if lastRequest.usageAvailable}
                <span>Token usage is available in View usage.</span>
              {:else}
                <span>This request has no recorded token usage.</span>
              {/if}
            {:else if hasUsedKey}
              <strong>Proxy key accepted</strong>
              <span>Wait for the request to finish. If no result appears, check the client and proxy logs.</span>
            {:else}
              <strong>Waiting for first request</strong>
              <span>Send a request from your client. If nothing appears, check the base URL and whether your key is active below.</span>
            {/if}
          </div>
          {#if hasActiveKey && lastRequestFailed}
            <Button class="secondary-button investigate-button" title="View failed requests (Enter)" aria-keyshortcuts="Enter" onclick={() => viewUsage(clientId, 'failed')} onkeydown={guardEnter}>View failed requests</Button>
          {/if}
        </div>
      </div>
    </section>
    <section class="client-table" aria-labelledby="keys-title">
      <header class="section-head"><h2 id="keys-title" tabindex="-1">Proxy keys</h2></header>
      <div class="table-scroll">
        <table class="keys-table">
          <thead>
            <tr><th scope="col">Key prefix</th><th scope="col" class="key-date">Created</th><th scope="col" class="key-date">Last used</th><th scope="col">Status</th><th scope="col"><span class="sr-only">Actions</span></th></tr>
          </thead>
          <tbody>
            {#each selected.keys as key (key.id)}
              <tr>
                <td class="key-identity">
                  <code>{key.prefix}…</code>
                  <dl class="key-dates muted">
                    <div><dt>Created</dt><dd>{formatDateTime(key.createdAt)}</dd></div>
                    <div><dt>Last used</dt><dd>{key.lastUsedAt ? formatDateTime(key.lastUsedAt) : 'Never'}</dd></div>
                  </dl>
                </td>
                <td class="key-date">{formatDateTime(key.createdAt)}</td>
                <td class="key-date">{key.lastUsedAt ? formatDateTime(key.lastUsedAt) : 'Never'}</td>
                <td><span class="key-status" class:revoked={key.revokedAt}>{key.revokedAt ? 'Revoked' : 'Active'}</span></td>
                <td>
                  {#if !key.revokedAt}
                    <Button class="secondary-button compact-button" aria-label={'Revoke key ' + key.prefix} aria-keyshortcuts="Delete" title="Revoke key (Delete)" disabled={!canManageClients()} focusableWhenDisabled onclick={(event) => confirmRevoke(key, event)} onkeydown={revokeShortcut}>Revoke</Button>
                  {/if}
                </td>
              </tr>
            {:else}
              <tr><td colspan="5" class="muted">No proxy keys.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {:else if !loading && !loadError}
    <h1 id="client-title" tabindex="-1">Client unavailable</h1>
    <div class="empty-state">Select a client from the list.</div>
  {/if}
{:else if active}
  <header class="page-head">
    <div>
      <h1 id="clients-title" tabindex="-1">Clients</h1>
      {#if clients.length || (!loading && !loadError)}<p>{clients.length} {clients.length === 1 ? 'client' : 'clients'}</p>{/if}
    </div>
    <Button id="new-client" class="primary-button" aria-keyshortcuts="N" title="New client (N when focused)"
      disabled={!canCreate()} focusableWhenDisabled onclick={openCreate} onkeydown={(event) => clientShortcut(event, 'N')}>
      <PlusIcon size={16} aria-hidden="true" /> New client <kbd aria-hidden="true">N</kbd>
    </Button>
  </header>
  {#if clients.length}
    <section class="client-table" aria-label="Client list">
      <div class="table-scroll">
        <table class="clients-table">
          <thead>
            <tr><th scope="col">Client</th><th scope="col">Active keys</th><th scope="col" class="client-date">Created</th><th scope="col"><span class="sr-only">Actions</span></th></tr>
          </thead>
          <tbody>
            {#each clients as client (client.id)}
              <tr>
                <td><span class="client-name"><KeyRoundIcon size={15} aria-hidden="true" /><span title={client.name}>{client.name}</span></span></td>
                <td>{activeKeys(client)}</td>
                <td class="client-date">{formatDateTime(client.createdAt)}</td>
                <td>
                  <Button id={'manage-client-' + client.id} class="icon-button" aria-label={'Manage client ' + client.name} onclick={(event) => openClient(client.id, event)}>
                    <ChevronRightIcon size={17} aria-hidden="true" />
                  </Button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {:else if !loading && !loadError}
    <div class="empty-state">
      <KeyRoundIcon size={22} aria-hidden="true" />
      <p>No clients yet. Create a client to issue its first proxy key.</p>
    </div>
  {/if}
{/if}

{#if active && loading && !loadError}
  <div class="empty-state" role="status">Loading clients…</div>
{/if}

<Dialog bind:open={dialogOpen} closeOnOutsidePress={!generated || copyState === 'copied'} onOpenChange={(open, details) => {
  if (!open && (busy || (generated && copyState !== 'copied' && details.returnValue !== 'saved'))) details.cancel();
  else if (!open) generated = null;
}}>
  <DialogBackdrop class="dialog-backdrop" />
  <DialogContent class="dialog-content" initialFocus={() => document.getElementById(generated ? 'copy-key' : 'client-name')} finalFocus={keyDialogReturnFocus}>
    {#if generated}
      <DialogTitle class="dialog-title">New proxy key</DialogTitle>
      <DialogDescription class="muted">Copy this key now. The full key is shown only once.</DialogDescription>
      <div class="key-secret" data-agent-hidden>
        <input id="issued-key" aria-label="New proxy key" value={generated.token} readonly />
        <Button id="copy-key" class="secondary-button" title="Copy key (Enter)" aria-keyshortcuts="Enter" onclick={copyKey} onkeydown={guardEnter}>
          {#if copyState === 'copied'}<CheckIcon size={15} aria-hidden="true" />{:else}<CopyIcon size={15} aria-hidden="true" />{/if}
          Copy key
        </Button>
      </div>
      <p class="copy-status muted" aria-live="polite">
        {#if copyState === 'copied'}Key copied.{:else if copyState === 'manual'}Key selected. Use your system’s copy shortcut.{/if}
      </p>
      <p class="muted">Send it in the <code>X-LLM-Meter-Key</code> header.</p>
      <div class="dialog-actions key-dialog-actions">
        {#if copyState !== 'copied'}
          <AlertDialog
            onOpenChange={(open, details) => { discardKey = !open && details.returnValue === 'discard'; }}
            onOpenChangeComplete={(open) => {
              if (!open && discardKey) {
                dialogOpen = false;
                generated = null;
              }
            }}
          >
            <AlertDialogTrigger class="secondary-button" disabled={busy} onkeydown={guardEnter}>Close without saving</AlertDialogTrigger>
            <AlertDialogBackdrop class="dialog-backdrop" />
            <AlertDialogContent class="dialog-content">
              <AlertDialogTitle class="dialog-title">Close without saving the key?</AlertDialogTitle>
              <AlertDialogDescription class="muted">The key stays active. To see a full key again, issue a new one and revoke this key if you no longer need it.</AlertDialogDescription>
              <div class="dialog-actions key-dialog-actions">
                <AlertDialogClose class="secondary-button" onkeydown={guardEnter}>Cancel</AlertDialogClose>
                <AlertDialogClose class="danger-button" value="discard" aria-keyshortcuts="Enter" onkeydown={guardEnter}>Close without saving <kbd aria-hidden="true">↵</kbd></AlertDialogClose>
              </div>
            </AlertDialogContent>
          </AlertDialog>
        {/if}
        <DialogClose class="primary-button" value="saved" disabled={busy} aria-keyshortcuts="Enter" onkeydown={guardEnter}>
          {copyState === 'copied' ? 'Done' : 'I saved the key'} <kbd aria-hidden="true">↵</kbd>
        </DialogClose>
      </div>
    {:else if dialogMode === 'key'}
      <DialogTitle class="dialog-title">Generating proxy key</DialogTitle>
      <DialogDescription class="muted">The key will appear here when it’s ready.</DialogDescription>
    {:else}
      <DialogClose class="icon-button dialog-x" aria-label="Close new client form" disabled={busy}><XIcon size={19} aria-hidden="true" /></DialogClose>
      <DialogTitle class="dialog-title">New client</DialogTitle>
      <DialogDescription class="muted">Create a client and its first proxy key.</DialogDescription>
      <form class="dialog-form" onsubmit={createClient}>
        <Field class="form-field" name="name" invalid={clientNameTooLong ? true : undefined}>
          <FieldLabel>Client name</FieldLabel>
          <Input id="client-name" class="text-input" bind:value={draft} required placeholder="e.g. OpenCode" disabled={busy} onkeydown={guardEnter} />
          {#if clientNameTooLong}<FieldError class="field-error" match>Shorten the name to 100 characters or fewer.</FieldError>{/if}
        </Field>
        {#if createError}<RecoveryNotice problem={createError.problem} />{/if}
        <div class="dialog-actions">
          <DialogClose id="close-client-form" class="secondary-button" disabled={busy} aria-keyshortcuts="Escape"
            title={createError?.uncertain ? 'Back to clients (Escape)' : 'Cancel (Escape)'} onkeydown={guardEnter}>
            {createError?.uncertain ? 'Back to clients' : 'Cancel'}
          </DialogClose>
          {#if !createError?.uncertain}
            <Button type="submit" class="primary-button" aria-keyshortcuts="Enter" disabled={!canSubmitClient()} onkeydown={guardEnter}>{busy ? 'Creating…' : 'Create client'} <kbd aria-hidden="true">↵</kbd></Button>
          {/if}
        </div>
      </form>
    {/if}
  </DialogContent>
</Dialog>

<AlertDialog bind:open={revokeOpen} onOpenChange={(open, details) => { if (!open && busy) details.cancel(); }}>
  <AlertDialogBackdrop class="dialog-backdrop" />
  <AlertDialogContent class="dialog-content" finalFocus={revokeDialogReturnFocus}>
    <AlertDialogTitle class="dialog-title">Revoke this key?</AlertDialogTitle>
    <AlertDialogDescription class="muted">New HTTP requests and WebSocket connections using <code>{revoking?.key.prefix}…</code> will be denied. The client’s usage history stays available.</AlertDialogDescription>
    {#if revokeError}<RecoveryNotice problem={revokeError} />{/if}
    <div class="dialog-actions">
      <AlertDialogClose class="secondary-button" disabled={busy} onkeydown={guardEnter}>Cancel</AlertDialogClose>
      <Button class="danger-button" disabled={busy} title="Confirm revocation (Enter)" aria-keyshortcuts="Enter"
        data-agent-confirm="Revokes this client key immediately." onclick={revoke} onkeydown={guardEnter}>{busy ? 'Revoking…' : 'Confirm revocation'}</Button>
    </div>
  </AlertDialogContent>
</AlertDialog>

<style>
  .client-crumb { margin-bottom: calc(var(--gap) * -.45); }
  .head-actions { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
  .client-table td { white-space: nowrap; }
  .client-table td:last-child { width: 1%; }
  .client-name { display: flex; align-items: center; gap: 9px; font-weight: 550; }
  .client-name > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .client-name :global(svg) { flex: none; color: var(--accent); }
  .connection { border-block-start: 1px solid var(--line); padding-block-start: var(--section-space); }
  .connection-columns { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 18px 32px; margin-block-start: 12px; }
  .connection-instructions { color: var(--muted); }
  .connection-status { display: grid; gap: 3px; }
  .connection-status strong { color: var(--text); }
  .request-result { color: var(--text); font-weight: 600; }
  .request-result.failed { color: var(--danger); }
  .connection-instructions :global(.investigate-button) { margin-block-start: 8px; }
  .key-status { color: var(--success); }
  .key-status.revoked { color: var(--muted); }
  .key-dates { display: none; }
  .key-secret { display: flex; gap: 6px; margin-top: 16px; }
  .key-secret input { flex: 1; min-width: 0; margin: 0; padding: 0 9px; border: 1px solid var(--control-border); border-radius: 6px; background: var(--bg); font: 13px var(--mono); }
  .key-secret :global(button) { flex: none; }
  .copy-status { min-height: 1.5em; margin: 4px 0; font-size: 12px; }
  .dialog-actions.key-dialog-actions { flex-wrap: wrap; }
  @media (max-width: 760px) {
    .clients-table { table-layout: fixed; }
    .clients-table .client-date { display: none; }
    .clients-table th:nth-child(2) { inline-size: 6.25rem; }
    .clients-table :is(th, td):last-child { inline-size: 56px; }
    .clients-table :global(.icon-button) { inline-size: 44px; block-size: 44px; }
    .connection-columns { grid-template-columns: minmax(0, 1fr); gap: 14px; }
    .keys-table .key-date { display: none; }
    .keys-table td { vertical-align: top; }
    .keys-table .key-identity { width: 100%; white-space: normal; overflow-wrap: anywhere; }
    .key-dates { display: grid; gap: 2px; margin: 4px 0 0; }
    .key-dates div { display: flex; flex-wrap: wrap; gap: 0 5px; }
    .key-dates dd { margin: 0; }
    .keys-table :global(.compact-button) { min-height: var(--touch-target); }
  }
</style>
