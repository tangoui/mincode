// Closing Element Plus boxes keep their dimensions during leave transitions.
// Target the newest active box instead of clicking a previous dialog again.
export function activeSmokeDialog(root: ParentNode): HTMLElement | undefined {
  return [...root.querySelectorAll<HTMLElement>(".el-message-box")].reverse().find(el =>
    el.getBoundingClientRect().width > 0 && !el.closest('[class*="-leave-"]'),
  );
}
