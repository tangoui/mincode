import { describe, expect, it, vi } from "vitest";
import { useFileTemplatesStore } from "@/stores/file-templates";
import { prepareTemplateWrites } from "@/services/file-template-service";
import { templateVariables } from "@/domain/projects/file-templates";
const bridge = vi.hoisted(() => ({ invoke: vi.fn(), desktop: true }));
vi.mock("@/services/desktop-bridge", () => ({ invokeBackend: (...args: unknown[]) => bridge.invoke(...args), isDesktopRuntime: () => bridge.desktop, nativeErrorMessage: String }));
const file = { path: "{{mincode.namePascal}}.txt", source: "a".repeat(64), size: 5 };
const summary = { id: "one", revision: 1, kind: "file", title: "Mine", description: "", category: "", defaultName: "Example", files: [file] };
describe("custom template resources", () => {
    it("preserves custom templates when plugin contributions are revoked", async () => {
        bridge.invoke.mockResolvedValue({ version: 1, revision: 1, templates: [summary] });
        const store = useFileTemplatesStore();
        await store.hydrateCustom();
        store.setContributed([]);
        expect(store.templates).toHaveLength(1);
        expect(store.templates[0]?.origin).toBe("custom");
    });
    it("rejects an edited selection before creating files", async () => {
        bridge.invoke.mockResolvedValueOnce({ version: 1, revision: 1, templates: [summary] }).mockResolvedValueOnce({ revision: 2, template: { ...summary, revision: 2, files: [{ path: file.path, content: "new" }] } });
        const store = useFileTemplatesStore();
        await store.hydrateCustom();
        await expect(prepareTemplateWrites(store.templates[0]!, templateVariables("Demo", "project"), "")).rejects.toThrow();
    });
    it("does not turn failed native reads into browser success", async () => {
        bridge.invoke.mockRejectedValue(new Error("disk unavailable"));
        const store = useFileTemplatesStore();
        await expect(store.hydrateCustom()).rejects.toThrow("disk unavailable");
        expect(store.templates).toHaveLength(0);
    });
});
it("does not report a committed save as failed when list refresh fails", async () => {
    bridge.invoke.mockReset();
    bridge.invoke.mockResolvedValueOnce({ revision: 1, template: { ...summary, files: [{ path: file.path, content: "saved" }] } }).mockRejectedValueOnce(new Error("refresh unavailable"));
    const store = useFileTemplatesStore();
    const result = await store.saveCustom(0, { ...summary, kind: "file", files: [{ path: file.path, content: "saved" }] });
    expect(result.template.id).toBe("one");
    expect(store.custom.revision).toBe(1);
    expect(store.custom.templates[0]?.id).toBe("one");
});
it("rejects malformed document revisions and duplicate target paths", async () => {
    const { parseCustomDocument, parseCustomSnapshot } = await import('@/domain/projects/custom-file-templates');
    const body = { ...summary, files: [{ path: 'a.txt', content: 'x' }] };
    expect(() => parseCustomDocument({ revision: 0, template: body })).toThrow();
    expect(() => parseCustomDocument({ revision: 1, template: { ...body, kind: 'project', files: [{ path: 'A.txt', content: 'x' }, { path: 'a.txt', content: 'y' }] } })).toThrow();
    expect(() => parseCustomSnapshot({ version: 2, revision: 1, templates: [] })).toThrow();
    expect(() => parseCustomSnapshot({ version: 1, revision: NaN, templates: [] })).toThrow();
});
it("retains earlier created files and stops on the first failure", async () => {
    const { applyTemplateWrites } = await import('@/services/file-template-service');
    bridge.invoke.mockReset();
    bridge.invoke.mockResolvedValueOnce(null).mockRejectedValueOnce(new Error('already_exists'));
    const result = await applyTemplateWrites('/fixture', [{ filePath: 'a', relativePath: 'a', content: 'one', bytes: 3 }, { filePath: 'b', relativePath: 'b', content: 'two', bytes: 3 }, { filePath: 'c', relativePath: 'c', content: 'three', bytes: 5 }]);
    expect(result.created).toEqual(['a']);
    expect(result.failed?.filePath).toBe('b');
    expect(bridge.invoke.mock.calls).toHaveLength(2);
    expect(bridge.invoke.mock.calls.every(([cmd, args]) => cmd === 'local_fs_write_text_file' && args.createOnly === true)).toBe(true);
});
it("late hydration cannot replace a newer mutation", async () => {
    bridge.invoke.mockReset();
    let resolve: (v: unknown) => void = () => { };
    bridge.invoke.mockImplementationOnce(() => new Promise(r => { resolve = r; }));
    const store = useFileTemplatesStore();
    store.custom = { version: 1, revision: 1, templates: [] };
    const pending = store.hydrateCustom();
    bridge.invoke.mockResolvedValueOnce({ revision: 2, template: { ...summary, revision: 2, files: [{ path: file.path, content: 'saved' }] } });
    await store.saveCustom(1, { ...summary, kind: 'file', files: [{ path: file.path, content: 'saved' }] });
    resolve({ version: 1, revision: 1, templates: [] });
    await pending;
    expect(store.custom.revision).toBe(2);
    expect(store.custom.templates).toHaveLength(1);
});

it("rechecks plugin source authority instead of reusing verified cached bytes", async () => {
    const { loadTemplateSources } = await import('@/services/file-template-service');
    const { releasePluginAssets } = await import('@/services/contribution-assets');
    const bytes = new TextEncoder().encode('hello');
    const key = [...new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))].map(b => b.toString(16).padStart(2, '0')).join('');
    const item = { key: 'fixture.example', pluginId: 'fixture', pluginName: 'Fixture', id: 'example', kind: 'file' as const, title: 'Example', description: '', category: '', defaultName: 'Example', files: [{ path: 'hello.txt', source: key, size: 5 }] };
    releasePluginAssets('fixture');
    bridge.invoke.mockReset();
    bridge.invoke.mockResolvedValueOnce({ assets: [{ key, mime: 'text/plain', data: 'aGVsbG8=' }], missing: [], stale: [], deferred: [] });
    expect((await loadTemplateSources(item)).get(key)).toBe('hello');
    bridge.invoke.mockResolvedValueOnce({ assets: [], missing: [], stale: [key], deferred: [] });
    await expect(loadTemplateSources(item)).rejects.toThrow('changed or are unavailable');
    releasePluginAssets('fixture');
});

it("does not label a partial stale library as a complete newer snapshot", async () => {
    bridge.invoke.mockReset();
    const removed = { ...summary, id: 'removed' };
    const added = { ...summary, id: 'added', revision: 2 };
    bridge.invoke.mockResolvedValueOnce({ version: 1, revision: 1, templates: [summary, removed] });
    const store = useFileTemplatesStore();
    await store.hydrateCustom();
    // A get in another window/editor has observed revision 2; this store has not.
    bridge.invoke.mockResolvedValueOnce({ revision: 3, template: { ...summary, revision: 3, files: [{ path: file.path, content: 'saved' }] } });
    bridge.invoke.mockResolvedValueOnce({ version: 1, revision: 3, templates: [{ ...summary, revision: 3 }, added] });
    await store.saveCustom(2, { ...summary, kind: 'file', files: [{ path: file.path, content: 'saved' }] });
    expect(store.custom.templates.map(t => t.id).sort()).toEqual(['added', 'one']);
    expect(store.custom.revision).toBe(3);
});

it("reconciles the complete library after deleting from a newer editor revision", async () => {
    bridge.invoke.mockReset();
    bridge.invoke.mockResolvedValueOnce({ version: 1, revision: 1, templates: [summary] });
    const store = useFileTemplatesStore();
    await store.hydrateCustom();
    bridge.invoke.mockResolvedValueOnce({ revision: 3 });
    bridge.invoke.mockResolvedValueOnce({ version: 1, revision: 3, templates: [{ ...summary, id: 'added', revision: 2 }] });
    await store.deleteCustom(2, 'one');
    expect(store.custom.templates.map(t => t.id)).toEqual(['added']);
    expect(store.custom.revision).toBe(3);
});

it("keeps a confirmed mutation successful but marks failed gap reconciliation", async () => {
    bridge.invoke.mockReset();
    const store = useFileTemplatesStore();
    bridge.invoke.mockResolvedValueOnce({ revision: 3, template: { ...summary, revision: 3, files: [{ path: file.path, content: 'saved' }] } });
    bridge.invoke.mockRejectedValueOnce(new Error('list unavailable'));
    const saved = await store.saveCustom(2, { ...summary, kind: 'file', files: [{ path: file.path, content: 'saved' }] });
    expect(saved.revision).toBe(3);
    expect(store.custom.revision).toBe(0);
    expect(store.error).toContain('list unavailable');
});
