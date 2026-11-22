<script lang="ts">
  import CheckIcon from '@lucide/svelte/icons/check';
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
  import SearchIcon from '@lucide/svelte/icons/search';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button } from '@ui-sdk/svelte/button';
  import { createSelectController, Select, SelectClear, SelectContent, SelectEmpty, SelectInput, SelectItem, SelectItemIndicator, SelectList, SelectTrigger, SelectValue } from '@ui-sdk/svelte/select';
  import { guardEnter } from './format';

  type Option = { value: string; label: string };
  type Props = {
    label: string;
    items: Option[];
  } & (
    { multiple?: false; value: string; onChange: (value: string) => void } |
    { multiple: true; value: string[]; placeholder: string; onChange: (value: string[]) => void }
  );

  let props: Props = $props();
  let control = $state<HTMLDivElement>();
  const selection = createSelectController<string, true>();
  const filterName = $derived(props.label.toLowerCase());
  const clearLabel = $derived('Clear ' + filterName + ' filter');
  const selected = $derived.by(() => {
    if (!props.multiple || !props.value.length) return [];
    const labels = new Map(props.items.map(({ value, label }) => [value, label]));
    return props.value.map((value) => ({ value, label: labels.get(value) ?? value }));
  });

  function changeValue(next: string | string[] | null) {
    if (props.multiple && Array.isArray(next)) props.onChange(next);
    else if (!props.multiple && typeof next === 'string') props.onChange(next);
  }

  function removeValue(value: string, event: MouseEvent) {
    const next = selection.getValue().filter((item) => item !== value);
    if (!selection.setValue(next)) return;
    const chip = (event.currentTarget as HTMLButtonElement).parentElement;
    const neighbor = chip?.nextElementSibling ?? chip?.previousElementSibling;
    const button = neighbor?.querySelector('button');
    if (button) button.focus({ preventScroll: true });
    else selection.focus();
  }

  function guardOptionKey(event: KeyboardEvent) {
    const typing = event.target instanceof HTMLInputElement;
    if (typing && (event.isComposing || event.keyCode === 229)) {
      event.stopPropagation();
      return;
    }
    guardEnter(event, !typing);
    if (event.defaultPrevented) event.stopPropagation();
  }
</script>

{#snippet options(items: Option[])}
  {#each items as item (item.value)}
    <SelectItem class="select-item" value={item.value} label={item.label}>
      <span>{item.label}</span>
      <SelectItemIndicator><CheckIcon size={14} aria-hidden="true" /></SelectItemIndicator>
    </SelectItem>
  {/each}
{/snippet}

<Select multiple={props.multiple} value={props.value} items={props.items} controller={props.multiple ? selection : undefined}
  placeholder={props.multiple ? props.placeholder : undefined} disabled={props.multiple && !props.items.length} onValueChange={changeValue}>
  <div class="filter-controls" class:multiple={props.multiple} class:has-selections={selected.length > 0} bind:this={control}>
    <SelectTrigger class="filter-trigger" aria-label={props.label} onkeydown={guardEnter}>
      <SelectValue class={selected.length ? 'sr-only' : undefined} />
      <ChevronDownIcon size={14} aria-hidden="true" />
    </SelectTrigger>
    {#if selected.length}
      <div class="filter-chips">
        {#each selected as { value, label } (value)}
          <span class="filter-chip">
            <span class="filter-chip-label">{label}</span>
            <Button class="filter-chip-remove" aria-label={'Remove ' + label} aria-keyshortcuts="Enter Space"
              title={'Remove ' + label + ' (Enter or Space)'} onclick={(event) => removeValue(value, event)} onkeydown={guardEnter}>
              <XIcon size={12} aria-hidden="true" />
            </Button>
          </span>
        {/each}
      </div>
      <SelectClear class="icon-button filter-clear" aria-label={clearLabel} aria-keyshortcuts="Enter Space"
        title="Clear all selections (Enter or Space)" onkeydown={guardEnter}>
        <XIcon size={16} aria-hidden="true" />
      </SelectClear>
    {/if}
  </div>
  <SelectContent class={props.multiple ? 'select-content filter-search-popup' : 'select-content'}
    anchor={props.multiple ? control : undefined} onkeydowncapture={guardOptionKey}>
    {#if props.multiple}
      <div class="filter-search">
        <span aria-hidden="true"><SearchIcon size={14} /></span>
        <SelectInput class="text-input" aria-label={'Search ' + filterName + 's'} placeholder="Search…" />
      </div>
      <SelectEmpty><span class="filter-search-empty">No matches found.</span></SelectEmpty>
      <SelectList class="filter-search-results">
        {#snippet children({ items })}
          {@render options(items as Option[])}
        {/snippet}
      </SelectList>
    {:else}
      {@render options(props.items)}
    {/if}
  </SelectContent>
</Select>

<style>
  .filter-controls { display: flex; align-items: center; gap: 4px; }
  .filter-controls.multiple {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: 0;
    max-inline-size: 360px;
    background: var(--surface);
    border: 1px solid var(--control-border);
    border-radius: 6px;
  }
  .multiple :global(.filter-trigger) {
    grid-column: 1 / -1;
    grid-row: 1;
    min-block-size: 32px;
    max-inline-size: none;
    border: 0;
    background: transparent;
  }
  .has-selections :global(.filter-trigger) {
    grid-column: 3;
    align-self: stretch;
    justify-content: center;
    min-inline-size: 30px;
    padding-inline: 6px;
    border-inline-start: 1px solid var(--strong-line);
    border-start-start-radius: 0;
    border-end-start-radius: 0;
  }
  .multiple :global(.filter-trigger:hover), .multiple :global(.filter-clear:hover) { background: var(--hover); }
  .multiple :global(.filter-clear) {
    grid-column: 2;
    grid-row: 1;
    background: transparent;
    border: 0;
    border-radius: 4px;
    margin-inline: 2px;
  }
  .filter-chips {
    grid-column: 1;
    grid-row: 1;
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    min-inline-size: 0;
    padding: 3px 4px;
  }
  .filter-chip {
    display: inline-flex;
    align-items: center;
    max-inline-size: 100%;
    min-inline-size: 0;
    border: 1px solid var(--strong-line);
    border-radius: 4px;
    padding-inline-start: 6px;
    background: var(--nav-current);
    color: var(--nav-current-text);
    font-size: 12px;
  }
  .filter-chip-label { min-inline-size: 0; overflow-wrap: anywhere; }
  .filter-chip :global(.filter-chip-remove) {
    display: grid;
    place-items: center;
    align-self: stretch;
    flex: none;
    min-inline-size: 24px;
    min-block-size: 24px;
    padding: 0;
    border: 0;
    border-radius: 3px;
    background: transparent;
    cursor: pointer;
  }
  .filter-chip :global(.filter-chip-remove:hover) { background: var(--option-highlighted); }
  @media (pointer: coarse) {
    .has-selections :global(.filter-trigger), .filter-chip :global(.filter-chip-remove) {
      min-inline-size: var(--touch-target);
      min-block-size: var(--touch-target);
    }
  }
  :global(.select-content.filter-search-popup) {
    display: flex;
    flex-direction: column;
    min-inline-size: min(90vw, max(220px, var(--anchor-width, 0px)));
    overflow: hidden;
  }
  .filter-search { position: relative; flex: none; margin: 3px 3px 6px; }
  .filter-search > span {
    position: absolute;
    inset-inline-start: 9px;
    inset-block-start: 50%;
    transform: translateY(-50%);
    display: flex;
    color: var(--muted);
    pointer-events: none;
  }
  .filter-search :global(input) { inline-size: 100%; padding-inline-start: 29px; }
  .filter-search-empty { display: block; padding: 7px 8px; color: var(--muted); }
  :global(.filter-search-results) { min-block-size: 0; overflow-y: auto; overscroll-behavior: contain; }
</style>
