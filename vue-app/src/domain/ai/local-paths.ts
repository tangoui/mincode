const DRIVE_PATH = /^[A-Za-z]:[\\/]/;
const URI_SCHEME = /^[A-Za-z][\w+.-]*:/;
const INVALID_PATH = /\p{Cc}/u;
const PATH_WORD = /[^\s"'“”‘’<>「」【】()[\]{},，。：；、!?！？]+/gu;
const MAX_PATH_LENGTH = 4096;
const MAX_WORDS_PER_PATH = 12;

function absolute(path: string) {
  return path.startsWith("/") || DRIVE_PATH.test(path);
}

export function resolveAiLocalPath(raw: string, directory: string, home: string, fromHref = false): string | undefined {
  let path = raw.trim();
  if (fromHref) {
    try {
      if (/^file:/i.test(path)) {
        const url = new URL(path);
        if (url.host || url.search || url.hash) return;
        path = decodeURIComponent(url.pathname);
        if (/^\/[A-Za-z]:\//.test(path)) path = path.slice(1);
      } else {
        path = decodeURIComponent(path);
      }
    } catch {
      return;
    }
  }
  if (!path || path.length > MAX_PATH_LENGTH || INVALID_PATH.test(path) || /^[\\/]{2}/.test(path)) return;
  if (!DRIVE_PATH.test(path) && URI_SCHEME.test(path)) return;

  if (/^~[\\/]/.test(path)) {
    if (!absolute(home)) return;
    path = home + "/" + path.slice(2);
  } else if (!absolute(path)) {
    if (!absolute(directory)) return;
    path = directory + "/" + path;
  }
  const windows = DRIVE_PATH.test(path);
  const normalized = windows ? path.replace(/\\/g, "/") : path;
  const root = windows ? normalized.slice(0, 3) : "/";
  const segments: string[] = [];
  for (const segment of normalized.slice(root.length).split("/")) {
    if (segment === "..") segments.pop();
    else if (segment && segment !== ".") segments.push(segment);
  }
  return root + segments.join("/");
}

export function looksLikeInlinePath(text: string) {
  return /[\\/]/.test(text) || /^[^\s]+$/.test(text) || /\.[\p{L}\p{N}_-]+$/u.test(text);
}

export interface AiPathSpan {
  start: number;
  end: number;
  text: string;
}

function pathStart(word: string) {
  return absolute(word) || /^(?:~|\.{1,2})[\\/]/.test(word);
}

function looksLikeProsePath(word: string) {
  if (pathStart(word)) return word !== "/";
  if (URI_SCHEME.test(word)) return false;
  return /[\\/]/.test(word) || /^[\p{L}\p{N}_.-]+\.[\p{L}\p{N}_-]+$/u.test(word);
}

/** Each group shares a starting offset. Check longer names first, falling back
 * to the shortest plausible path when adjacent prose is not part of the name. */
export function* aiProsePathCandidates(text: string): Generator<AiPathSpan[]> {
  if (!/[.\\/]/.test(text)) return;
  const words = Array.from(text.matchAll(PATH_WORD));
  for (const [index, word] of words.entries()) {
    const start = word.index;
    if (!looksLikeProsePath(word[0].replace(/[.:;]+$/, ""))) continue;
    const spans: AiPathSpan[] = [];
    let end = start;
    for (const part of words.slice(index, index + MAX_WORDS_PER_PATH)) {
      if (part !== word && (pathStart(part[0]) || !/^[ \t]+$/.test(text.slice(end, part.index)))) break;
      end = part.index + part[0].length;
      const value = text.slice(start, end).replace(/[.:;]+$/, "");
      if (value.length > MAX_PATH_LENGTH) break;
      spans.unshift({ start, end: start + value.length, text: value });
      if (value.length !== end - start) break;
    }
    if (spans.length) yield spans;
  }
}
