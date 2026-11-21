import { parseCustomDocument, parseCustomSnapshot, templateRecord, templateRevision, type CustomTemplateDraft } from "@/domain/projects/custom-file-templates";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
function desktop() { if (!isDesktopRuntime())
    throw new Error("Templates require the desktop app"); }
export async function listCustomTemplates() { desktop(); return parseCustomSnapshot(await invokeBackend("file_template_custom_list", {})); }
export async function getCustomTemplate(id: string) { desktop(); return parseCustomDocument(await invokeBackend("file_template_custom_get", { id })); }
export async function saveCustomTemplate(revision: number, template: CustomTemplateDraft) { desktop(); return parseCustomDocument(await invokeBackend("file_template_custom_save", { revision, template })); }
export async function deleteCustomTemplate(revision: number, id: string) { desktop(); return templateRevision(templateRecord(await invokeBackend("file_template_custom_delete", { revision, id })).revision); }
