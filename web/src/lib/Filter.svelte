<script lang="ts">
  import CheckIcon from '@lucide/svelte/icons/check';
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
  import XIcon from '@lucide/svelte/icons/x';
  import { Select, SelectClear, SelectContent, SelectItem, SelectItemIndicator, SelectTrigger, SelectValue } from '@ui-sdk/svelte/select';
  import { guardEnter } from './format';

  type Props = {
    label: string;
    items: { value: string; label: string }[];
  } & (
    { multiple?: false; value: string; onChange: (value: string) => void } |
    { multiple: true; value: string[]; placeholder: string; onChange: (value: string[]) => void }
  );

  let props: Props = $props();
  const filterName = $derived(props.label.toLowerCase());
  const clearLabel = $derived('Clear ' + filterName + ' filter');

  function changeValue(next: string | string[] | null) {
    if (props.multiple && Array.isArray(next)) props.onChange(next);
    else if (!props.multiple && typeof next === 'string') props.onChange(next);
  }

  function guardOptionKey(event: KeyboardEvent) {
    guardEnter(event, true);
    if (event.defaultPrevented) event.stopPropagation();
  }
</script>

<Select multiple={props.multiple} value={props.value} items={props.items}
  placeholder={props.multiple ? props.placeholder : undefined} disabled={props.multiple && !props.items.length} onValueChange={changeValue}>
  <div class="filter-controls">
    <SelectTrigger class="filter-trigger" aria-label={props.label} onkeydown={guardEnter}>
      {#if props.multiple}
        <SelectValue>
          {#snippet children({ label, labels, placeholder })}
            {placeholder ? props.placeholder : labels.length > 1 ? `${labels.length} ${filterName}s` : label}
          {/snippet}
        </SelectValue>
      {:else}
        <SelectValue />
      {/if}
      <ChevronDownIcon size={14} aria-hidden="true" />
    </SelectTrigger>
    {#if props.multiple}
      <SelectClear class="icon-button filter-clear" aria-label={clearLabel} aria-keyshortcuts="Enter Space"
        title={clearLabel + ' (Enter or Space)'} onkeydown={guardEnter}>
        <XIcon size={14} aria-hidden="true" />
      </SelectClear>
    {/if}
  </div>
  <SelectContent class="select-content" onkeydowncapture={guardOptionKey}>
    {#each props.items as item (item.value)}
      <SelectItem class="select-item" value={item.value}>
        <span>{item.label}</span>
        <SelectItemIndicator><CheckIcon size={14} aria-hidden="true" /></SelectItemIndicator>
      </SelectItem>
    {/each}
  </SelectContent>
</Select>

<style>
  .filter-controls { display: flex; align-items: center; gap: 4px; }
  .filter-controls :global(.filter-clear) { flex: none; }
</style>
