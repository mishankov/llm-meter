<script lang="ts">
  import { onMount, tick } from 'svelte';
  import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
  import ChartNoAxesCombinedIcon from '@lucide/svelte/icons/chart-no-axes-combined';
  import CheckIcon from '@lucide/svelte/icons/check';
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
  import CopyIcon from '@lucide/svelte/icons/copy';
  import KeyRoundIcon from '@lucide/svelte/icons/key-round';
  import PlusIcon from '@lucide/svelte/icons/plus';
  import XIcon from '@lucide/svelte/icons/x';
  import { AlertDialog, AlertDialogBackdrop, AlertDialogClose, AlertDialogContent, AlertDialogDescription, AlertDialogTitle } from '@ui-sdk/svelte/alert-dialog';
  import { Button } from '@ui-sdk/svelte/button';
  import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/svelte/dialog';
  import { Field, FieldLabel } from '@ui-sdk/svelte/field';
  import { Input } from '@ui-sdk/svelte/input';
  import { useShortcut } from '@ui-sdk/svelte/shortcuts';
  import { APIError, api, jsonPost, type Client, type Key, type NewKey } from './api';
  import { dismissShortcut, formatDateTime, guardEnter } from './format';

  interface Props {
    clientId: string;
    draft?: string;
    navigate: (clientId?: string) => void;
    viewUsage: (clientId: string) => void;
    onUnauthorized: () => void;
  }
  let { clientId, draft = $bindable(''), navigate, viewUsage, onUnauthorized }: Props = $props();

  let clients = $state<Client[]>([]);
  let selected = $derived(clients.find((client) => client.id === clientId));
  let loading = $state(true);
  let loadError = $state('');
  let actionError = $state('');
  let createError = $state('');
  let busy = $state(false);
  let dialogOpen = $state(false);
  let dialogMode = $state<'create' | 'key'>('create');
  let revokeOpen = $state(false);
  let generated = $state<NewKey | null>(null);
  let revoking = $state<Key | null>(null);
  let copyState = $state<'idle' | 'copied' | 'manual'>('idle');
  let listRequest: AbortController | null = null;
  let returnFocus = $state<HTMLElement | null>(null);

  function message(cause: unknown) {
    if (cause instanceof APIError && cause.status === 401) onUnauthorized();
    return cause instanceof Error ? cause.message : 'Could not complete the request.';
  }

  async function load(silent = false) {
    if (silent && (loading || busy || dialogOpen || revokeOpen || listRequest)) return;
    listRequest?.abort();
    const current = new AbortController();
    listRequest = current;
    if (!silent) {
      loading = true;
      loadError = '';
    }
    try {
      const result = await api<{ clients: Client[] }>('/clients', { signal: current.signal });
      if (!current.signal.aborted) clients = result.clients;
    } catch (cause) {
      if (current.signal.aborted) return;
      if (!silent) loadError = message(cause);
      else if (cause instanceof APIError && cause.status === 401) onUnauthorized();
    } finally {
      if (listRequest === current) {
        listRequest = null;
        if (!silent) loading = false;
      }
    }
  }

  onMount(() => {
    void load();
    const timer = window.setInterval(() => { if (!document.hidden) void load(true); }, 5_000);
    return () => {
      window.clearInterval(timer);
      listRequest?.abort();
    };
  });

  function canCreate() {
    return !loading && !loadError && !busy && !dialogOpen && !revokeOpen;
  }

  function openCreate() {
    if (!canCreate()) return;
    listRequest?.abort();
    returnFocus = document.activeElement as HTMLElement | null;
    generated = null;
    createError = '';
    dialogMode = 'create';
    dialogOpen = true;
  }

  async function createClient(event: SubmitEvent) {
    event.preventDefault();
    if (!draft.trim() || busy) return;
    listRequest?.abort();
    busy = true;
    createError = '';
    try {
      const result = await api<{ client: Client; key: NewKey }>('/clients', jsonPost({ name: draft.trim() }));
      clients = [result.client, ...clients];
      draft = '';
      navigate(result.client.id);
      await reveal(result.key);
    } catch (cause) {
      createError = message(cause);
    } finally {
      busy = false;
    }
  }

  async function makeKey() {
    if (!selected || !canCreate()) return;
    listRequest?.abort();
    const id = selected.id;
    returnFocus = document.activeElement as HTMLElement | null;
    busy = true;
    actionError = '';
    dialogMode = 'key';
    dialogOpen = true;
    try {
      const result = await api<{ key: NewKey }>('/clients/' + encodeURIComponent(id) + '/keys', { method: 'POST' });
      const { token, ...metadata } = result.key;
      clients = clients.map((client) => client.id === id ? { ...client, keys: [metadata, ...client.keys] } : client);
      await reveal({ ...metadata, token });
    } catch (cause) {
      actionError = message(cause);
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
    if (!generated) return;
    try {
      await navigator.clipboard.writeText(generated.token);
      copyState = 'copied';
    } catch {
      const token = document.getElementById('issued-key');
      if (token instanceof HTMLInputElement) {
        token.focus();
        token.select();
      }
      copyState = 'manual';
    }
  }

  function confirmRevoke(key: Key) {
    if (busy || dialogOpen) return;
    listRequest?.abort();
    revoking = key;
    revokeOpen = true;
    actionError = '';
  }

  function revokeShortcut(event: KeyboardEvent, key: Key) {
    guardEnter(event);
    if (event.key === 'Delete' && !dismissShortcut(event)) {
      event.preventDefault();
      confirmRevoke(key);
    }
  }

  async function revoke() {
    if (!selected || !revoking || busy) return;
    listRequest?.abort();
    const id = selected.id;
    const keyId = revoking.id;
    busy = true;
    try {
      await api('/clients/' + encodeURIComponent(id) + '/keys/' + encodeURIComponent(keyId), { method: 'DELETE' });
      clients = clients.map((client) => client.id === id ? {
        ...client, keys: client.keys.map((key) => key.id === keyId ? { ...key, revokedAt: new Date().toISOString() } : key)
      } : client);
      revokeOpen = false;
      await tick();
      document.getElementById('new-key')?.focus();
    } catch (cause) {
      actionError = message(cause);
    } finally {
      busy = false;
    }
  }

  useShortcut('n', openCreate, { when: () => canCreate() && !clientId, description: 'New client' });
  useShortcut('k', makeKey, { when: () => canCreate() && Boolean(selected), description: 'New key' });

  function physicalShortcut(event: KeyboardEvent) {
    if (event.defaultPrevented || dismissShortcut(event) || /^[a-z]$/i.test(event.key) || event.key === 'Dead' || !canCreate()) return;
    if (!clientId && event.code === 'KeyN') {
      event.preventDefault();
      openCreate();
    } else if (selected && event.code === 'KeyK') {
      event.preventDefault();
      void makeKey();
    }
  }

  function activeKeys(client: Client) {
    return client.keys.filter((key) => key.revokedAt === null).length;
  }

  function openClient(id: string) {
    navigate(id);
    void load(true);
  }
</script>

<svelte:window onkeydown={physicalShortcut} />

{#if clientId}
  <div class="client-crumb">
    <Button class="secondary-button back-button" onclick={() => navigate()}>
      <ArrowLeftIcon size={15} aria-hidden="true" /> Clients
    </Button>
  </div>
  {#if selected}
    <header class="page-head">
      <div><h1>{selected.name}</h1><p>Created {formatDateTime(selected.createdAt)}</p></div>
      <div class="head-actions">
        <Button class="secondary-button" onclick={() => viewUsage(selected?.id ?? '')}>
          <ChartNoAxesCombinedIcon size={15} aria-hidden="true" /> View usage
        </Button>
        <Button id="new-key" class="primary-button" aria-keyshortcuts="K" disabled={!canCreate()} onclick={makeKey}>
          <PlusIcon size={16} aria-hidden="true" /> New key <kbd aria-hidden="true">K</kbd>
        </Button>
      </div>
    </header>
    {#if actionError && !revokeOpen}<div class="feedback error-feedback" role="alert">{actionError}</div>{/if}
    <section class="panel client-panel" aria-labelledby="keys-title">
      <header class="panel-head"><h2 id="keys-title">Proxy keys</h2></header>
      <div class="table-scroll">
        <table>
          <thead>
            <tr><th scope="col">Key prefix</th><th scope="col">Created</th><th scope="col">Last used</th><th scope="col">Status</th><th scope="col"><span class="sr-only">Actions</span></th></tr>
          </thead>
          <tbody>
            {#each selected.keys as key (key.id)}
              <tr>
                <td><code>{key.prefix}…</code></td>
                <td>{formatDateTime(key.createdAt)}</td>
                <td>{key.lastUsedAt ? formatDateTime(key.lastUsedAt) : 'Never'}</td>
                <td><span class="key-status" class:revoked={key.revokedAt}>{key.revokedAt ? 'Revoked' : 'Active'}</span></td>
                <td>
                  {#if !key.revokedAt}
                    <Button class="secondary-button compact-button" aria-label={'Revoke key ' + key.prefix} aria-keyshortcuts="Delete" title="Revoke key (Delete)" disabled={busy} onclick={() => confirmRevoke(key)} onkeydown={(event) => revokeShortcut(event, key)}>Revoke <kbd aria-hidden="true">Del</kbd></Button>
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
    <h1>Client unavailable</h1>
    <div class="panel empty-state">Select a client from the list.</div>
  {/if}
{:else}
  <header class="page-head">
    <div>
      <h1>Clients</h1>
      {#if !loading && !loadError}<p>{clients.length} {clients.length === 1 ? 'client' : 'clients'}</p>{/if}
    </div>
    <Button id="new-client" class="primary-button" aria-keyshortcuts="N" disabled={!canCreate()} onclick={openCreate}>
      <PlusIcon size={16} aria-hidden="true" /> New client <kbd aria-hidden="true">N</kbd>
    </Button>
  </header>
  {#if clients.length && !loadError}
    <section class="panel client-panel" aria-label="Client list">
      <div class="table-scroll">
        <table>
          <thead>
            <tr><th scope="col">Client</th><th scope="col">Active keys</th><th scope="col">Created</th><th scope="col"><span class="sr-only">Actions</span></th></tr>
          </thead>
          <tbody>
            {#each clients as client (client.id)}
              <tr>
                <td><span class="client-name"><KeyRoundIcon size={15} aria-hidden="true" /> {client.name}</span></td><td>{activeKeys(client)}</td>
                <td>{formatDateTime(client.createdAt)}</td>
                <td>
                  <Button class="icon-button" aria-label={'Manage client ' + client.name} onclick={() => openClient(client.id)}>
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
    <div class="panel empty-state">
      <KeyRoundIcon size={22} aria-hidden="true" />
      <p>No clients yet. Create a client to issue its first proxy key.</p>
    </div>
  {/if}
{/if}

{#if loadError}
  <div class="feedback error-feedback" role="alert"><span>{loadError}</span><Button class="secondary-button" onclick={() => load()}>Retry</Button></div>
{:else if loading}
  <div class="panel empty-state" role="status">Loading clients…</div>
{/if}

<Dialog bind:open={dialogOpen} onOpenChange={(open, details) => {
  if (!open && busy) details.cancel();
  else if (!open) generated = null;
}}>
  <DialogBackdrop class="dialog-backdrop" />
  <DialogContent class="dialog-content" initialFocus={() => document.getElementById(generated ? 'copy-key' : 'client-name')} finalFocus={() => returnFocus?.isConnected ? returnFocus : document.getElementById('new-key')}>
    {#if generated}
      <DialogTitle class="dialog-title">New proxy key</DialogTitle>
      <DialogDescription class="muted">Copy this key now. The full key is shown only once.</DialogDescription>
      <div class="key-secret" data-agent-hidden>
        <input id="issued-key" aria-label="New proxy key" value={generated.token} readonly />
        <Button id="copy-key" class="secondary-button" onclick={copyKey}>
          {#if copyState === 'copied'}<CheckIcon size={15} aria-hidden="true" />{:else}<CopyIcon size={15} aria-hidden="true" />{/if}
          Copy key
        </Button>
      </div>
      <p class="copy-status muted" aria-live="polite">
        {#if copyState === 'copied'}Key copied.{:else if copyState === 'manual'}Key selected. Use your system’s copy shortcut.{/if}
      </p>
      <p class="muted">Send it in the <code>X-LLM-Proxy-Key</code> header.</p>
      <div class="dialog-actions"><DialogClose class="primary-button" disabled={busy}>Done</DialogClose></div>
    {:else if dialogMode === 'key'}
      <DialogTitle class="dialog-title">Generating proxy key</DialogTitle>
      <DialogDescription class="muted">The key will appear here when it’s ready.</DialogDescription>
    {:else}
      <DialogClose class="icon-button dialog-x" aria-label="Close new client form" disabled={busy}><XIcon size={19} aria-hidden="true" /></DialogClose>
      <DialogTitle class="dialog-title">New client</DialogTitle>
      <DialogDescription class="muted">Create a client and its first proxy key.</DialogDescription>
      <form class="dialog-form" onsubmit={createClient}>
        <Field class="form-field" name="name">
          <FieldLabel>Client name</FieldLabel>
          <Input id="client-name" class="text-input" bind:value={draft} required placeholder="e.g. OpenCode" disabled={busy} onkeydown={guardEnter} />
        </Field>
        {#if createError}<p class="field-error" role="alert">{createError}</p>{/if}
        <div class="dialog-actions">
          <DialogClose class="secondary-button" disabled={busy}>Cancel</DialogClose>
          <Button type="submit" class="primary-button" aria-keyshortcuts="Enter" disabled={busy || !draft.trim()} onkeydown={guardEnter}>{busy ? 'Creating…' : 'Create client'} <kbd aria-hidden="true">↵</kbd></Button>
        </div>
      </form>
    {/if}
  </DialogContent>
</Dialog>

<AlertDialog bind:open={revokeOpen} onOpenChange={(open, details) => { if (!open && busy) details.cancel(); }}>
  <AlertDialogBackdrop class="dialog-backdrop" />
  <AlertDialogContent class="dialog-content">
    <AlertDialogTitle class="dialog-title">Revoke this key?</AlertDialogTitle>
    <AlertDialogDescription class="muted">New HTTP requests and WebSocket connections using <code>{revoking?.prefix}…</code> will be denied. The client’s usage history stays available.</AlertDialogDescription>
    {#if actionError}<p class="field-error" role="alert">{actionError}</p>{/if}
    <div class="dialog-actions">
      <AlertDialogClose class="secondary-button" disabled={busy} onkeydown={guardEnter}>Cancel</AlertDialogClose>
      <Button class="danger-button" disabled={busy} data-agent-confirm="Revokes this client key immediately." onclick={revoke} onkeydown={guardEnter}>{busy ? 'Revoking…' : 'Confirm revocation'}</Button>
    </div>
  </AlertDialogContent>
</AlertDialog>

<style>
  .client-crumb { margin-bottom: calc(var(--gap) * -.45); }
  .head-actions { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
  .client-panel td { white-space: nowrap; }
  .client-panel td:last-child { width: 1%; }
  .client-panel .table-scroll { min-height: 0; }
  .client-panel .panel-head { padding-bottom: 11px; }
  .client-name { display: inline-flex; align-items: center; gap: 9px; font-weight: 550; }
  .client-name :global(svg) { color: var(--accent); }
  .key-status { color: var(--success); }
  .key-status.revoked { color: var(--muted); }
  .key-secret { display: flex; gap: 6px; margin-top: 16px; }
  .key-secret input { flex: 1; min-width: 0; margin: 0; padding: 0 9px; border: 1px solid var(--strong-line); border-radius: 6px; background: var(--bg); font: 13px var(--mono); }
  .key-secret :global(button) { flex: none; }
  .copy-status { min-height: 1.5em; margin: 4px 0; font-size: 11px; }
</style>
