/** Opt-in desktop test, isolated HOME and disposable files only. */
import { createApp, nextTick, ref } from "vue";
import { getActivePinia } from "pinia";
import CustomFileTemplatesDialog from "@/components/projects/CustomFileTemplatesDialog.vue";
import { useFileTemplatesStore } from "@/stores/file-templates";
import { getCustomTemplate } from "@/services/custom-file-template-service";
import { prepareTemplateWrites, applyTemplateWrites } from "@/services/file-template-service";
import { buildPluginFileTemplateRegistry, templateVariables } from "@/domain/projects/file-templates";
import { fetchPluginContributions, installLocalPlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { invokeBackend } from "@/services/desktop-bridge";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { pointContributions } from "@/domain/plugins/contributions";
import { loadTemplateSources } from "@/services/file-template-service";
import { activeSmokeDialog } from "@/services/e2e-dialogs";
async function until<T>(read: () => T | undefined | false, label: string): Promise<T> { const end = Date.now() + 15000; while (Date.now() < end) {
    const v = read();
    if (v)
        return v;
    await new Promise(r => setTimeout(r, 50));
} throw new Error(`template smoke timeout: ${label}`); }
export async function fileTemplatesScenario(config: SmokeConfig, log: {
    done(step: string): void;
}) {
    const fixture = config.fileTemplates!, store = useFileTemplatesStore();
    const read = (filePath: string) => invokeBackend<string>("local_fs_read_text_file", { projectRoot: fixture.projectRoot, filePath });
    const apply = async (title: string, parent: string) => { await store.refreshCustom(); const item = store.templates.find(t => t.origin === 'custom' && t.title === title); if (!item)
        throw new Error('custom template missing'); const writes = await prepareTemplateWrites(item, templateVariables('Demo', 'Demo'), parent); const result = await applyTemplateWrites(fixture.projectRoot, writes); if (result.failed)
        throw new Error(`write failed ${result.failed.message}`); return writes; };
    if (fixture.phase === 'cold') {
        await store.hydrateCustom();
        store.setContributed([]);
        await apply('Native custom', 'cold');
        if (await read('cold/Demo.txt') !== 'Edited Demo\n')
            throw new Error('cold content differs');
        await apply('Native project', 'cold-project');
        if (await read('cold-project/src/Demo.txt') !== 'Project Demo\n')
            throw new Error('cold project differs');
        log.done('application cold restart retained edited file and multi-file templates without plugin');
        return;
    }
    await installLocalPlugin(fixture.packagePath);
    await setPluginEnabled('file-templates', true);
    const snapshot = await fetchPluginContributions(['fileTemplates']);
    const registry = buildPluginFileTemplateRegistry(pointContributions(snapshot, 'fileTemplates'));
    if (registry.templates.length !== 56 || registry.issues.length)
        throw new Error('expanded catalog not registered');
    store.setContributed(registry.templates);
    const official = registry.templates.find(t => t.id === 'html-page')!;
    const sources = await loadTemplateSources(official);
    const copied = await store.saveCustom(0, { id: '', revision: 0, kind: official.kind, title: 'Copied official', description: official.description, category: official.category, defaultName: official.defaultName, files: official.files.map(f => ({ path: f.path, content: sources.get(f.source)! })) });
    await store.deleteCustom(store.custom.revision, copied.template.id);
    log.done('all 56 installed templates admitted; official sources hash-verified and copied independently');
    await store.hydrateCustom();
    const host = document.createElement('div');
    document.body.append(host);
    const visible = ref(true);
    const app = createApp(CustomFileTemplatesDialog, { modelValue: true, 'onUpdate:modelValue': (v: boolean) => { visible.value = v; } });
    const pinia = getActivePinia();
    if (pinia)
        app.use(pinia);
    app.mount(host);
    const confirm = setInterval(() => { activeSmokeDialog(document)?.querySelector<HTMLButtonElement>('.el-message-box__btns .el-button--primary')?.click(); }, 80);
    const button = async (text: string) => { const b = await until(() => [...document.querySelectorAll<HTMLButtonElement>('.el-dialog button')].find(b => b.textContent?.trim() === text && !b.disabled), text); b.click(); await nextTick(); };
    const input = async (selector: string, value: string) => { const el = await until(() => document.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector) ?? undefined, selector); el.value = value; el.dispatchEvent(new Event('input', { bubbles: true })); await nextTick(); };
    try {
        await button('新建文件模板');
        await input('[data-test="template-title"] input', 'Native custom');
        await input('.custom-file-templates__editor textarea', 'Original {{mincode.name}}\n');
        await button('保存模板');
        await until(() => store.custom.templates.find(t => t.title === 'Native custom'), 'saved file');
        await input('.custom-file-templates__editor textarea', 'Edited {{mincode.name}}\n');
        await button('保存模板');
        await until(() => !document.querySelector<HTMLButtonElement>('.el-dialog button.is-loading'), 'save complete');
        const first = store.custom.templates.find(t => t.title === 'Native custom')!;
        const saved = await getCustomTemplate(first.id);
        if (saved.template.files[0]?.content !== 'Edited {{mincode.name}}\n')
            throw new Error('UI edit not saved');
        log.done('real management dialog created and edited custom file through native durable commands');
        await apply('Native custom', 'generated');
        if (await read('generated/Demo.txt') !== 'Edited Demo\n')
            throw new Error('generated file differs');
        const writes = await prepareTemplateWrites(store.custom.templates.find(t => t.id === first.id)!, templateVariables('Demo', 'Demo'), 'generated');
        const collision = await applyTemplateWrites(fixture.projectRoot, writes);
        if (!collision.failed?.alreadyExists)
            throw new Error('existing file overwritten');
        const created = await store.saveCustom(store.custom.revision, { id: '', revision: 0, title: 'Native project', kind: 'project', description: '', category: '', defaultName: 'Demo', files: [{ path: 'src/{{mincode.namePascal}}.txt', content: 'Project {{mincode.name}}\n' }, { path: 'README.md', content: 'Readme\n' }] });
        await apply('Native project', 'generated-project');
        if (await read('generated-project/README.md') !== 'Readme\n')
            throw new Error('project differs');
        const temporary = await store.saveCustom(store.custom.revision, { ...created.template, id: '', revision: 0, title: 'Copy to delete' });
        await store.deleteCustom(store.custom.revision, temporary.template.id);
        await setPluginEnabled('file-templates', false);
        await uninstallPlugin('file-templates', true);
        store.setContributed([]);
        await apply('Native custom', 'without-plugin');
        if (await read('without-plugin/Demo.txt') !== 'Edited Demo\n')
            throw new Error('plugin removal erased custom');
        log.done('custom project/copy/delete persisted; createOnly refused existing file; plugin uninstall kept resources');
    }
    finally {
        clearInterval(confirm);
        app.unmount();
        host.remove();
    }
}
