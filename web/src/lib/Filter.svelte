<script lang="ts">
  import CheckIcon from '@lucide/svelte/icons/check';
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
  import { Select, SelectContent, SelectItem, SelectItemIndicator, SelectTrigger, SelectValue } from '@ui-sdk/svelte/select';

  interface Props {
    label: string;
    value: string;
    items: { value: string; label: string }[];
    onChange: (value: string) => void;
  }

  let { label, value, items, onChange }: Props = $props();
</script>

<Select {value} {items} onValueChange={(next) => { if (next !== null) onChange(next); }}>
  <SelectTrigger class="filter-trigger" aria-label={label}>
    <SelectValue />
    <ChevronDownIcon size={14} aria-hidden="true" />
  </SelectTrigger>
  <SelectContent class="select-content">
    {#each items as item (item.value)}
      <SelectItem class="select-item" value={item.value}>
        <span>{item.label}</span>
        <SelectItemIndicator><CheckIcon size={14} aria-hidden="true" /></SelectItemIndicator>
      </SelectItem>
    {/each}
  </SelectContent>
</Select>
