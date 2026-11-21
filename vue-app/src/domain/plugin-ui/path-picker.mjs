// Validates a plugin view's pick-path request and filters what the native
// dialog returned, before any path reaches the page.

export const MAX_PICKED_PATHS = 1000;
const MAX_EXTENSIONS = 32;
const REQUEST_ID = /^[a-zA-Z0-9._:-]{1,128}$/;
const EXTENSION = /^\.[a-z0-9]{1,16}$/;

export function parsePathPickRequest(value) {
  if (!value || typeof value !== "object") return undefined;
  if (typeof value.id !== "string" || !REQUEST_ID.test(value.id)) return undefined;
  if (value.kind !== "file" && value.kind !== "directory") return undefined;
  let extensions = [];
  if (value.extensions !== undefined) {
    if (!Array.isArray(value.extensions) || value.extensions.length > MAX_EXTENSIONS) return undefined;
    extensions = [...new Set(value.extensions
      .filter(extension => typeof extension === "string")
      .map(extension => extension.toLowerCase())
      .filter(extension => EXTENSION.test(extension)))];
  }
  return { id: value.id, kind: value.kind, multiple: value.kind === "file" && value.multiple === true, extensions };
}

function extensionAccepted(extensions, path) {
  if (extensions.length === 0) return true;
  const name = path.split(/[\\/]/).pop() ?? "";
  const dot = name.lastIndexOf(".");
  return dot > 0 && extensions.includes(name.slice(dot).toLowerCase());
}

export function filterPickedPaths(paths, extensions, max) {
  return paths
    .filter(path => typeof path === "string" && path.length > 0 && extensionAccepted(extensions, path))
    .slice(0, max);
}

// A sandboxed page can post pick-path without the user doing anything. A click
// in the page activates the host document too (user activation propagates to
// ancestor frames), so a request right after a real click opens the native
// dialog at once; without one the host confirms first. The native dialog
// itself still needs the user to choose.
export function pathPickNeedsConfirmation(activation) {
  return activation?.isActive !== true;
}
