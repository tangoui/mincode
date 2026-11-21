/**
 * Focus and select an inline rename input once the menu that opened it has fully closed.
 *
 * - `el-input` uses `inheritAttrs: false`, so `data-*` attributes land on the native `<input>`
 *   itself; match either the input or a wrapper that contains it.
 * - `el-dropdown` closes on a timer and its focus trap restores focus to the trigger when it
 *   releases. Focusing too early gets stolen back (and the input's blur would save and exit
 *   rename), so wait one macrotask after Vue has flushed before focusing.
 */
export function focusInlineInput(selector: string) {
  const find = () => {
    const el = document.querySelector<HTMLElement>(selector);
    if (!el) return null;
    return el instanceof HTMLInputElement ? el : el.querySelector<HTMLInputElement>("input");
  };
  const focus = () => {
    const input = find();
    if (!input || document.activeElement === input) return;
    input.focus({ preventScroll: true });
    input.select();
  };
  window.setTimeout(focus, 0);
}
