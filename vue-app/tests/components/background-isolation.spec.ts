import { afterEach, describe, expect, it } from "vitest";
import { isolateBackground } from "@/composables/use-background-isolation";

function element(tag: string, id: string) {
  const node = document.createElement(tag);
  node.id = id;
  document.body.append(node);
  return node;
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("isolateBackground", () => {
  it("makes every sibling inert except the kept surface", () => {
    const app = element("div", "app");
    const dialog = element("div", "teleported-dialog");
    const lock = element("div", "lock");

    const release = isolateBackground(lock);

    expect(app.hasAttribute("inert")).toBe(true);
    expect(dialog.hasAttribute("inert")).toBe(true);
    expect(lock.hasAttribute("inert")).toBe(false);
    release();
  });

  it("isolates layers appended while locked and restores them on release", async () => {
    const app = element("div", "app");
    const lock = element("div", "lock");
    const release = isolateBackground(lock);

    const drawer = element("div", "late-drawer");
    await Promise.resolve();
    expect(drawer.hasAttribute("inert")).toBe(true);

    release();
    expect(app.hasAttribute("inert")).toBe(false);
    expect(drawer.hasAttribute("inert")).toBe(false);
  });

  it("keeps elements that were already inert before locking", () => {
    const disabled = element("div", "already-inert");
    disabled.setAttribute("inert", "");
    const lock = element("div", "lock");

    isolateBackground(lock)();

    expect(disabled.hasAttribute("inert")).toBe(true);
  });

  it("does not make an ancestor of the kept surface inert", () => {
    const wrapper = element("div", "wrapper");
    const lock = document.createElement("div");
    wrapper.append(lock);
    const app = element("div", "app");

    const release = isolateBackground(lock);

    expect(wrapper.hasAttribute("inert")).toBe(false);
    expect(app.hasAttribute("inert")).toBe(true);
    release();
  });
});
