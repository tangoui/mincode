import { mount, flushPromises } from "@vue/test-utils";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { nextTick } from "vue";
import LargeTextPreview from "@/components/projects/LargeTextPreview.vue";
import ExternalFilePreview from "@/components/projects/ExternalFilePreview.vue";
import { invokeBackend } from "@/services/desktop-bridge";
import { applySavedLocale } from "@/i18n/messages";
import type { ProjectEditorDocument } from "@/domain/projects/types";

vi.mock("@/services/desktop-bridge", () => ({ invokeBackend: vi.fn() }));

const doc = (path = "/tmp/large.sql", sessionId = 1): ProjectEditorDocument => ({
  path, name: "large.sql", sessionId, external: true, externalKind: "binary",
  loaded: true, loading: false, content: "", savedContent: "", size: 70 * 1024 * 1024,
  textPreviewAvailable: true,
});
const first = { text: "first\n", startOffset: 0, nextOffset: 6, totalBytes: 13, eof: false, version: "v1" };
const second = { text: "second\n", startOffset: 6, nextOffset: 13, totalBytes: 13, eof: true, version: "v1" };
function deferred() {
  let resolve!: (value: unknown) => void;
  const promise = new Promise<unknown>((yes) => { resolve = yes; });
  return { promise, resolve };
}
function button(wrapper: ReturnType<typeof mount>, label: string) {
  const found = wrapper.findAll("button").find((b) => b.text() === label);
  if (!found) throw new Error(`missing ${label}`);
  return found;
}
beforeEach(async () => { await applySavedLocale("en"); vi.mocked(invokeBackend).mockReset().mockResolvedValue(first); });

describe("large text preview", () => {
  it("loads bounded pages, navigates back, and never puts partial text in the document", async () => {
    vi.mocked(invokeBackend).mockImplementation(async (_cmd, args) => (args as { offset: number }).offset === 6 ? second : first);
    const document = doc();
    const w = mount(LargeTextPreview, { props: { document, active: true } });
    await flushPromises();
    expect(w.find("pre").text()).toBe("first");
    expect(w.text()).toContain("Read-only");
    expect(button(w, "Previous").attributes("disabled")).toBeDefined();
    await button(w, "Next").trigger("click"); await flushPromises();
    expect(w.find("pre").text()).toBe("second");
    expect(button(w, "Next").attributes("disabled")).toBeDefined();
    expect(invokeBackend).toHaveBeenLastCalledWith("local_fs_read_external_text_chunk", { path: document.path, offset: 6, expectedVersion: "v1" }, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    await button(w, "Previous").trigger("click"); await flushPromises();
    expect(w.find("pre").text()).toBe("first");
    await w.trigger("keydown", { key: "s", ctrlKey: true });
    expect(document.content).toBe(""); expect(document.savedContent).toBe("");
    expect(vi.mocked(invokeBackend).mock.calls.every(([command]) => command === "local_fs_read_external_text_chunk")).toBe(true);
    w.unmount();
  });

  it("stops on file changes and reloads from zero with no old version", async () => {
    const w = mount(LargeTextPreview, { props: { document: doc() } }); await flushPromises();
    vi.mocked(invokeBackend).mockRejectedValueOnce(new Error("RuntimeError: file_changed"));
    await button(w, "Next").trigger("click"); await flushPromises();
    expect(w.text()).toContain("file has changed");
    expect(w.find("pre").exists()).toBe(false);
    expect(button(w, "Next").attributes("disabled")).toBeDefined();
    await button(w, "Reload").trigger("click"); await flushPromises();
    expect(invokeBackend).toHaveBeenLastCalledWith("local_fs_read_external_text_chunk", { path: "/tmp/large.sql", offset: 0 }, expect.anything());
    expect(w.find("pre").text()).toBe("first");
    w.unmount();
  });

  it("cancels on document replacement and ignores the old response even at the same path", async () => {
    const old = deferred(); vi.mocked(invokeBackend).mockReturnValueOnce(old.promise);
    const w = mount(LargeTextPreview, { props: { document: doc() } });
    await nextTick();
    const signal = (vi.mocked(invokeBackend).mock.calls[0][2] as { signal: AbortSignal }).signal;
    await w.setProps({ document: doc("/tmp/large.sql", 2) }); await flushPromises();
    expect(signal.aborted).toBe(true);
    old.resolve({ ...first, text: "stale\n" }); await flushPromises();
    expect(w.find("pre").text()).toBe("first");
    w.unmount();
  });

  it("cancels when hidden or closed, keeps no stale page and restarts on activation", async () => {
    const pending = deferred(); vi.mocked(invokeBackend).mockReturnValueOnce(pending.promise);
    const w = mount(LargeTextPreview, { props: { document: doc(), active: true } }); await nextTick();
    const signal = (vi.mocked(invokeBackend).mock.calls[0][2] as { signal: AbortSignal }).signal;
    await w.setProps({ active: false });
    expect(signal.aborted).toBe(true);
    pending.resolve(first); await flushPromises();
    expect(w.find("pre").exists()).toBe(false);
    await w.setProps({ active: true }); await flushPromises();
    expect(w.find("pre").text()).toBe("first");
    const closing = deferred(); vi.mocked(invokeBackend).mockReturnValueOnce(closing.promise);
    await button(w, "Next").trigger("click");
    const lastSignal = (vi.mocked(invokeBackend).mock.calls[vi.mocked(invokeBackend).mock.calls.length - 1][2] as { signal: AbortSignal }).signal;
    w.unmount(); expect(lastSignal.aborted).toBe(true);
    closing.resolve(second); await flushPromises();
  });

  it("reports invalid text and failed reads without rendering HTML or editable content", async () => {
    vi.mocked(invokeBackend).mockRejectedValueOnce(new Error("not_a_text_file"));
    const w = mount(LargeTextPreview, { props: { document: doc() } }); await flushPromises();
    expect(w.text()).toContain("UTF-8");
    vi.mocked(invokeBackend).mockResolvedValueOnce({ text: "<img src=x onerror=alert(1)>", startOffset: 0, nextOffset: 28, totalBytes: 28, eof: true, version: "v2" });
    await button(w, "Reload").trigger("click"); await flushPromises();
    expect(w.find("img").exists()).toBe(false);
    expect(w.find("pre").text()).toContain("<img");
    expect(w.find("textarea, [contenteditable=true], .monaco-editor").exists()).toBe(false);
    w.unmount();
  });

  it("the existing preview automatically shows eligible text while other binaries stay unchanged", async () => {
    const w = mount(ExternalFilePreview, { props: { document: doc() } });
    await vi.waitFor(() => expect(w.find("pre").exists()).toBe(true));
    expect(w.find("pre").text()).toBe("first");
    w.unmount();
    vi.mocked(invokeBackend).mockClear();
    const other = mount(ExternalFilePreview, { props: { document: { ...doc(), textPreviewAvailable: false } } }); await flushPromises();
    expect(other.find("pre").exists()).toBe(false);
    expect(invokeBackend).not.toHaveBeenCalled();
    other.unmount();
  });
});
