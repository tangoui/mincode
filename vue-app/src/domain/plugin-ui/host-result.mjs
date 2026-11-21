const MAX_HOST_FILE_BYTES = 256 * 1024;
const MAX_HOST_URL_BYTES = 2 * 1024;
const encoder = new globalThis.TextEncoder();

function record(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : undefined;
}

export function parsePluginHostSaveTextResult(value) {
  const raw = record(value);
  const file = record(raw?.file);
  if (!raw || raw.type !== 'save-text-file' || !file
    || typeof file.filename !== 'string' || !file.filename.trim() || file.filename.length > 255
    || file.filename === '.' || file.filename === '..'
    || file.filename.includes('/') || file.filename.includes('\\')
    || [...file.filename].some(character => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127)
    || typeof file.content !== 'string'
    || encoder.encode(file.content).byteLength > MAX_HOST_FILE_BYTES
    || encoder.encode(JSON.stringify(raw)).byteLength > MAX_HOST_FILE_BYTES) {
    throw new Error('Invalid plugin host file result');
  }
  return {
    filename: file.filename,
    content: file.content,
    savedResult: raw.savedResult,
    cancelledResult: raw.cancelledResult,
  };
}

export function parsePluginHostOpenURLResult(value) {
  const raw = record(value);
  const rawURL = raw?.url;
  if (!raw || raw.type !== 'open-url' || typeof rawURL !== 'string'
    || encoder.encode(rawURL).byteLength > MAX_HOST_URL_BYTES
    || !/^https?:\/\//i.test(rawURL)
    || /\s|\\/.test(rawURL)
    || /^https?:\/\/[^/?#]*@/i.test(rawURL)) {
    throw new Error('Invalid plugin host URL result');
  }
  let url;
  try {
    url = new globalThis.URL(rawURL);
  } catch {
    throw new Error('Invalid plugin host URL result');
  }
  if ((url.protocol !== 'http:' && url.protocol !== 'https:')
    || !url.hostname || url.username || url.password) {
    throw new Error('Invalid plugin host URL result');
  }
  return url.href;
}

// `download: true` marks a file download (for example a presigned object URL). The host
// opens downloads in the system browser because the built-in browser does not save files;
// every other link opens wherever the host prefers. Call only on a parsed open-url result.
export function isPluginHostDownloadResult(value) {
  return record(value)?.download === true;
}
