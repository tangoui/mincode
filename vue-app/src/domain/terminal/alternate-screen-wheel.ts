/**
 * xterm has no scrollback in the alternate screen used by TUIs. If the TUI
 * did not enable mouse tracking, a wheel event would otherwise be a no-op.
 * PageUp/PageDown are the portable fallback understood by terminal TUIs.
 */
export function alternateScreenWheelInput(
  bufferType: "normal" | "alternate",
  mouseTrackingMode: "none" | "x10" | "vt200" | "drag" | "any",
  deltaY: number,
  modified = false,
) {
  if (bufferType !== "alternate" || mouseTrackingMode !== "none" || modified || !Number.isFinite(deltaY) || deltaY === 0) return null;
  return deltaY < 0 ? "\u001b[5~" : "\u001b[6~";
}
