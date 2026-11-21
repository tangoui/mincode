import { mount, flushPromises } from "@vue/test-utils";
import { describe, it, expect, vi } from "vitest";
import CustomFileTemplatesDialog from "@/components/projects/CustomFileTemplatesDialog.vue";
const native = vi.hoisted(() => ({ invoke: vi.fn() }));
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, invokeBackend: (...a: unknown[]) => native.invoke(...a), nativeErrorMessage: String }));
describe("custom template manager", () => {
    it("can author the first template without a plugin and preserves failed drafts", async () => {
        native.invoke.mockImplementation(async (command: string) => { if (command === "file_template_custom_list")
            return { version: 1, revision: 0, templates: [] }; throw new Error("disk full"); });
        const wrapper = mount(CustomFileTemplatesDialog, { props: { modelValue: true }, attachTo: document.body });
        await flushPromises();
        const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.includes(text));
        button("新建文件模板")?.click();
        await flushPromises();
        expect(document.body.textContent).toContain("模板内容");
        const title = document.querySelector<HTMLInputElement>('[data-test="template-title"] input');
        expect(title).toBeTruthy();
        if (title) {
            title.value = "My template";
            title.dispatchEvent(new Event('input', { bubbles: true }));
        }
        button("保存模板")?.click();
        await flushPromises();
        expect(document.body.textContent).toContain("disk full");
        expect(title?.value).toBe("My template");
        wrapper.unmount();
    });
});
it("keeps dirty text after a revision conflict and requires explicit reload", async () => {
    native.invoke.mockReset();
    native.invoke.mockImplementation(async (command: string) => {
        if (command === 'file_template_custom_list')
            return { version: 1, revision: 0, templates: [] };
        throw new Error('file_template_revision_conflict');
    });
    const wrapper = mount(CustomFileTemplatesDialog, { props: { modelValue: true }, attachTo: document.body });
    await flushPromises();
    const click = async (text: string) => { [...document.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.trim() === text)?.click(); await flushPromises(); };
    await click('新建文件模板');
    const title = document.querySelector<HTMLInputElement>('[data-test="template-title"] input')!;
    title.value = 'Unmerged draft';
    title.dispatchEvent(new Event('input', { bubbles: true }));
    await flushPromises();
    await click('保存模板');
    expect(document.body.textContent).toContain('file_template_revision_conflict');
    expect(title.value).toBe('Unmerged draft');
    expect(document.body.textContent).toContain('另存为新模板');
    wrapper.unmount();
});

it("edits multiple file bodies without losing the other file", async () => {
 const {default:Editor}=await import('@/components/projects/CustomFileTemplateEditor.vue');
 const draft={id:'',revision:0,kind:'project' as const,title:'Project',description:'',category:'',defaultName:'Demo',files:[{path:'first.txt',content:'first'}]};
 const wrapper=mount(Editor,{props:{modelValue:draft,busy:false},attachTo:document.body});
 const add=wrapper.findAll('button').find(b=>b.text().includes('添加文件'))!;
 await add.trigger('click');
 expect(draft.files).toHaveLength(2);
 await wrapper.get('textarea').setValue('second content');
 expect(draft.files[0]?.content).toBe('first');expect(draft.files[1]?.content).toBe('second content');
 wrapper.unmount();
});
