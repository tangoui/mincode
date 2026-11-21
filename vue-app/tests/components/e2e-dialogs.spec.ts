import { describe, expect, it, vi } from "vitest";
import { activeSmokeDialog } from "@/services/e2e-dialogs";

function dialog(leaving = false, width = 300) {
  const overlay = document.createElement("div");
  if (leaving) overlay.className = "fade-in-linear-leave-active";
  const box = document.createElement("div");
  box.className = "el-message-box";
  vi.spyOn(box, "getBoundingClientRect").mockReturnValue({ width } as DOMRect);
  overlay.appendChild(box);
  document.body.appendChild(overlay);
  return box;
}

describe("native smoke confirmation targeting", () => {
  it("ignores a closing dialog that still has dimensions", () => {
    dialog(true);
    expect(activeSmokeDialog(document)).toBeUndefined();
    const current = dialog();
    expect(activeSmokeDialog(document)).toBe(current);
  });

  it("uses the newest active dialog and ignores hidden boxes", () => {
    dialog();
    const current = dialog();
    dialog(false, 0);
    expect(activeSmokeDialog(document)).toBe(current);
    current.remove();
    expect(activeSmokeDialog(document)).toBeDefined();
  });
});
