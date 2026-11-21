import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, onMounted, onUnmounted } from "vue";
import type { TerminalPaneNode } from "@/domain/terminal/types";

const counts = vi.hoisted(() => ({ mounts: new Map<string, number>(), unmounts: new Map<string, number>() }));

function terminalStub(name: string) {
  return defineComponent({
    name,
    props: { sessionId: { type: String, required: true }, active: Boolean },
    emits: ["select", "close", "split"],
    setup(props, { emit }) {
      onMounted(() => counts.mounts.set(props.sessionId, (counts.mounts.get(props.sessionId) ?? 0) + 1));
      onUnmounted(() => counts.unmounts.set(props.sessionId, (counts.unmounts.get(props.sessionId) ?? 0) + 1));
      return () => h("section", { class: "terminal-pane", "data-session": props.sessionId, onMousedown: () => emit("select") });
    },
  });
}

vi.mock("@/components/terminal/LocalTerminal.vue", () => ({ default: terminalStub("LocalTerminal") }));
vi.mock("@/components/terminal/SshTerminal.vue", () => ({ default: terminalStub("SshTerminal") }));

const { default: TerminalPaneLayout } = await import("@/components/terminal/TerminalPaneLayout.vue");

const leaf = (id: string, reloadVersion?: number): TerminalPaneNode => ({ id, type: "local", shellType: "system", reloadVersion });
const split = (id: string, children: TerminalPaneNode[]): TerminalPaneNode => ({ id, type: "split", direction: "horizontal", children, sizes: children.map(() => 100 / children.length) });

function mountLayout(node: TerminalPaneNode) {
  counts.mounts.clear();
  counts.unmounts.clear();
  return mount(TerminalPaneLayout, {
    props: { node, activePaneId: "a", canClose: true, canSplit: true },
    attachTo: document.body,
  });
}

const terminal = (id: string) => document.body.querySelector<HTMLElement>(`[data-session="${id}"]`);

describe("TerminalPaneLayout", () => {
  it("keeps a pane's terminal mounted when a split moves it under a new split group", async () => {
    const wrapper = mountLayout(leaf("a"));
    await flushPromises();
    expect(terminal("a")?.parentElement?.dataset.terminalPaneSlot).toBe("a");
    const before = terminal("a");

    await wrapper.setProps({ node: split("s1", [leaf("a"), leaf("b")]) });
    await flushPromises();
    await wrapper.setProps({ node: split("s1", [split("s2", [leaf("a"), leaf("c")]), leaf("b")]) });
    await flushPromises();

    // The same instance and DOM, now inside the nested split group's slot.
    expect(counts.mounts.get("a")).toBe(1);
    expect(counts.unmounts.get("a")).toBeUndefined();
    expect(terminal("a")).toBe(before);
    expect(terminal("a")?.parentElement?.dataset.terminalPaneSlot).toBe("a");
    expect(terminal("a")?.closest(".app-split-group .app-split-group")).not.toBeNull();
    expect(counts.mounts.get("b")).toBe(1);
    expect(counts.mounts.get("c")).toBe(1);

    // Closing a pane unmounts only that pane; the survivor moves back up.
    await wrapper.setProps({ node: split("s1", [leaf("a"), leaf("b")]) });
    await flushPromises();
    expect(counts.unmounts.get("c")).toBe(1);
    expect(counts.mounts.get("a")).toBe(1);
    expect(terminal("c")).toBeNull();
    wrapper.unmount();
  });

  it("rebuilds a pane on reload and forwards pane events with its id", async () => {
    const wrapper = mountLayout(split("s1", [leaf("a"), leaf("b")]));
    await flushPromises();
    await wrapper.setProps({ node: split("s1", [leaf("a", 1), leaf("b")]) });
    await flushPromises();
    expect(counts.mounts.get("a")).toBe(2);
    expect(counts.mounts.get("b")).toBe(1);

    terminal("b")?.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    expect(wrapper.emitted("select")).toEqual([["b"]]);
    wrapper.unmount();
    expect(terminal("a")).toBeNull();
  });
});
