<script lang="ts">
  import { browser } from '$app/env';
  import CheckIcon from '@lucide/svelte/icons/check';
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
  import SunMoonIcon from '@lucide/svelte/icons/sun-moon';
  import { Select, SelectContent, SelectItem, SelectItemIndicator, SelectTrigger, SelectValue } from '@ui-sdk/svelte/select';

  type Theme = 'light' | 'dark' | 'system';
  const themes: { value: Theme; label: string }[] = [
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
    { value: 'system', label: 'System' }
  ];

  let { disabled }: { disabled: boolean } = $props();
  const saved = browser ? document.documentElement.dataset.theme : undefined;
  let theme = $state<Theme>(saved === 'light' || saved === 'system' ? saved : 'dark');

  function changeTheme(next: Theme | null) {
    if (!next) return;
    theme = next;
    document.documentElement.dataset.theme = next;
    document.getElementById('light-theme-color')?.setAttribute('media',
      next === 'system' ? '(prefers-color-scheme: light)' : next === 'light' ? 'all' : 'not all');
    try {
      localStorage.setItem('llm-meter-theme', next);
    } catch {
      // Keep the current theme if the browser blocks storage.
    }
  }

  function guardThemeKey(event: KeyboardEvent) {
    if (event.isComposing || event.keyCode === 229 || event.repeat) {
      event.preventDefault();
      event.stopPropagation();
    }
  }
</script>

<Select items={themes} value={theme} {disabled} presentation="popover" onValueChange={changeTheme}>
  <SelectTrigger class="theme-trigger" aria-label="Theme" aria-keyshortcuts="Enter Space"
    title="Theme (Enter or Space)" onkeydowncapture={guardThemeKey}>
    <SunMoonIcon size={16} aria-hidden="true" />
    <SelectValue />
    <ChevronDownIcon class="theme-chevron" size={14} aria-hidden="true" />
  </SelectTrigger>
  <SelectContent class="select-content theme-options" sideOffset={4} onkeydowncapture={guardThemeKey}>
    {#each themes as { value, label } (value)}
      <SelectItem class="select-item" {value} {label}>
        {label}
        <SelectItemIndicator><CheckIcon size={14} aria-hidden="true" /></SelectItemIndicator>
      </SelectItem>
    {/each}
  </SelectContent>
</Select>
