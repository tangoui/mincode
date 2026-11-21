import { templateTargetPathError, isValidTemplateName, type FileTemplateKind, type PluginFileTemplateFile } from "./file-templates";
export interface CustomTemplateDraft {
    id: string;
    revision: number;
    kind: FileTemplateKind;
    title: string;
    description: string;
    category: string;
    defaultName: string;
    files: {
        path: string;
        content: string;
    }[];
}
export interface CustomTemplateSummary extends Omit<CustomTemplateDraft, "files"> {
    origin: "custom";
    key: string;
    descriptionLocalized?: Record<string, string>;
    titleLocalized?: Record<string, string>;
    files: PluginFileTemplateFile[];
}
export interface CustomTemplateSnapshot {
    version: 1;
    revision: number;
    templates: CustomTemplateSummary[];
}
export function templateRecord(v: unknown): Record<string, unknown> { if (!v || typeof v !== "object" || Array.isArray(v))
    throw new Error("Malformed template response"); return v as Record<string, unknown>; }
export function templateRevision(v: unknown): number { if (!Number.isSafeInteger(v) || Number(v) < 0)
    throw new Error("Malformed template revision"); return v as number; }
function validatePaths(files: {
    path: string;
}[]) {
    const seen: string[] = [];
    for (const file of files) {
        const path = file.path.toLowerCase();
        if (seen.some(old => old === path || old.startsWith(path + '/') || path.startsWith(old + '/')))
            throw new Error("Malformed template paths");
        seen.push(path);
    }
}
function metadata(raw: Record<string, unknown>) {
    if (typeof raw.id !== "string" || !raw.id || (raw.kind !== "file" && raw.kind !== "project"))
        throw new Error("Malformed template");
    for (const [key, max] of [["title", 64], ["description", 256], ["category", 24], ["defaultName", 64]] as const)
        if (typeof raw[key] !== "string" || [...raw[key] as string].length > max)
            throw new Error("Malformed template metadata");
    if (!(raw.title as string).trim() || !isValidTemplateName(raw.defaultName as string))
        throw new Error("Malformed template name");
    return { id: raw.id, revision: templateRevision(raw.revision), kind: raw.kind as FileTemplateKind, title: raw.title as string, description: raw.description as string, category: raw.category as string, defaultName: raw.defaultName as string };
}
export function parseCustomSummary(v: unknown): CustomTemplateSummary {
    const raw = templateRecord(v), meta = metadata(raw);
    if (!Array.isArray(raw.files) || !raw.files.length || raw.files.length > 64 || (meta.kind === "file" && raw.files.length !== 1))
        throw new Error("Malformed template files");
    const files = raw.files.map(v => { const f = templateRecord(v); if (typeof f.path !== "string" || templateTargetPathError(f.path) || typeof f.source !== "string" || !/^[a-f0-9]{64}$/.test(f.source) || !Number.isSafeInteger(f.size) || Number(f.size) < 0 || Number(f.size) > 262144)
        throw new Error("Malformed template file"); return { path: f.path, source: f.source, size: f.size as number }; });
    validatePaths(files);
    return { ...meta, origin: "custom", key: `custom:${meta.id}`, files };
}
export function parseCustomSnapshot(v: unknown): CustomTemplateSnapshot {
    const raw = templateRecord(v);
    if (raw.version !== 1 || !Array.isArray(raw.templates) || raw.templates.length > 64)
        throw new Error("Unsupported template library");
    const revision = templateRevision(raw.revision), templates = raw.templates.map(parseCustomSummary);
    if (new Set(templates.map(t => t.id)).size !== templates.length || templates.some(t => !t.revision || t.revision > revision))
        throw new Error("Malformed template library");
    return { version: 1, revision, templates };
}
export function parseCustomDocument(v: unknown): {
    revision: number;
    template: CustomTemplateDraft;
} {
    const raw = templateRecord(v), item = templateRecord(raw.template), meta = metadata(item);
    if (!Array.isArray(item.files) || !item.files.length || item.files.length > 64 || (meta.kind === "file" && item.files.length !== 1))
        throw new Error("Malformed template files");
    const files = item.files.map(v => { const f = templateRecord(v); if (typeof f.path !== "string" || templateTargetPathError(f.path) || typeof f.content !== "string" || new TextEncoder().encode(f.content).length > 262144)
        throw new Error("Malformed template body"); return { path: f.path, content: f.content }; });
    const revision = templateRevision(raw.revision);
    if (!meta.revision || meta.revision > revision || files.reduce((n, f) => n + new TextEncoder().encode(f.content).length, 0) > 2097152)
        throw new Error("Malformed template revision or size");
    validatePaths(files);
    return { revision, template: { ...meta, files } };
}
