// Applies a view's manifest fileDrop declaration (already validated by the
// backend) to what the native drop produced, before anything reaches the page.

const MAX_CONTENT_FILE_BYTES = 8 * 1024 * 1024;
const MAX_CONTENT_TOTAL_BYTES = 16 * 1024 * 1024;
// Base64 of MAX_CONTENT_FILE_BYTES.
const MAX_CONTENT_DATA_LENGTH = 11_184_812;

export function parseFileDrop(value) {
  if (!value || typeof value !== "object") return undefined;
  const mode = value.mode === "contents" || value.mode === "paths" ? value.mode : undefined;
  const maxEntries = Number.isSafeInteger(value.maxEntries) && value.maxEntries > 0 ? value.maxEntries : 0;
  if (!mode || !maxEntries) return undefined;
  const extensions = Array.isArray(value.extensions)
    ? value.extensions.filter(extension => typeof extension === "string" && /^\.[a-z0-9]{1,16}$/.test(extension))
    : [];
  return { mode, extensions, directories: mode === "paths" && value.directories === true, maxEntries };
}

function extensionAccepted(fileDrop, name) {
  if (fileDrop.extensions.length === 0) return true;
  const dot = name.lastIndexOf(".");
  return dot > 0 && fileDrop.extensions.includes(name.slice(dot).toLowerCase());
}

/**
 * @returns {{ ok: true, entries?: object[], files?: object[] } | { ok: false, reason: "none" | "tooMany" | "invalid", max?: number }}
 */
export function acceptDrop(fileDrop, event) {
  if (!fileDrop) return { ok: false, reason: "invalid" };
  if (fileDrop.mode === "paths") {
    if (!Array.isArray(event?.entries)) return { ok: false, reason: "invalid" };
    const entries = [];
    for (const entry of event.entries) {
      if (!entry || typeof entry.path !== "string" || typeof entry.name !== "string" || !entry.path || !entry.name) continue;
      if (!Number.isSafeInteger(entry.size) || entry.size < 0) continue;
      if (entry.kind === "directory" ? !fileDrop.directories : entry.kind !== "file" || !extensionAccepted(fileDrop, entry.name)) continue;
      entries.push({ path: entry.path, name: entry.name, kind: entry.kind, size: entry.size });
    }
    if (entries.length === 0) return { ok: false, reason: "none" };
    if (entries.length > fileDrop.maxEntries) return { ok: false, reason: "tooMany", max: fileDrop.maxEntries };
    return { ok: true, entries };
  }
  const files = event?.files;
  if (!Array.isArray(files) || files.length < 1) return { ok: false, reason: "invalid" };
  if (files.some(file => !file || typeof file.name !== "string" || typeof file.data !== "string"
    || !Number.isSafeInteger(file.size) || file.size < 0 || file.size > MAX_CONTENT_FILE_BYTES
    || !Number.isSafeInteger(file.lastModified) || file.data.length > MAX_CONTENT_DATA_LENGTH)
    || files.reduce((sum, file) => sum + file.size, 0) > MAX_CONTENT_TOTAL_BYTES) return { ok: false, reason: "invalid" };
  const accepted = files.filter(file => extensionAccepted(fileDrop, file.name));
  if (accepted.length === 0) return { ok: false, reason: "none" };
  if (accepted.length > fileDrop.maxEntries) return { ok: false, reason: "tooMany", max: fileDrop.maxEntries };
  return { ok: true, files: accepted.map(({ name, size, lastModified, data }) => ({ name, size, lastModified, data })) };
}
