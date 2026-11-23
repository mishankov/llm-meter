// Route a shortcut through its visible control so pointer and keyboard actions
// share form validation, disabled state, and the active dialog.
export function handleShortcut(event: KeyboardEvent) {
  if (event.defaultPrevented) return;
  const guarded = event.repeat || event.isComposing || event.keyCode === 229;
  if (!event.metaKey || event.altKey || event.ctrlKey) {
    if (guarded && !event.isComposing && event.key === "Enter") event.preventDefault();
    return;
  }

  let key = event.key.length === 1 ? event.key.toUpperCase() : event.key;
  // Use the physical Latin equivalent for layouts without Latin key values.
  if (!/^[A-Z,]$/.test(key) && /^Key[A-Z]$/.test(event.code)) key = event.code.slice(3);
  if (event.code === "Comma" && !event.shiftKey) key = ",";
  const shortcut = "Meta+" + (event.shiftKey ? "Shift+" : "") + key;
  if (activateShortcut(shortcut, guarded)) event.preventDefault();
}

export function activateShortcut(shortcut: string, guarded = false) {
  const visible = (element: HTMLElement) => element.getClientRects().length > 0;
  const popup = Array.from(document.querySelectorAll<HTMLElement>(
    '[role="dialog"], [role="alertdialog"], [role="listbox"]',
  )).find(visible);
  const controls = (popup ?? document).querySelectorAll<HTMLElement>("[aria-keyshortcuts]");
  const action = Array.from(controls).find(el => el.getAttribute("aria-keyshortcuts") === shortcut && visible(el));
  if (!action) return false;
  if (!guarded && !action.matches(":disabled, [aria-disabled=true], [data-disabled]")) action.click();
  return true;
}
