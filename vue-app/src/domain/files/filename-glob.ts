/**
 * Minimal filename glob used by Monaco-style `filenamePatterns`: `**` matches any
 * characters including `/`, `*` any characters except `/`, `?` one character
 * except `/`; everything else is literal. Matching is case-insensitive.
 */
export function globToRegExp(pattern: string) {
  let source = "^";
  for (let index = 0; index < pattern.length; index += 1) {
    const character = pattern[index];
    if (character === "*") {
      if (pattern[index + 1] === "*") {
        source += ".*";
        index += 1;
      } else {
        source += "[^/]*";
      }
    } else if (character === "?") {
      source += "[^/]";
    } else {
      source += character.replace(/[\\^$.*+?()[\]{}|]/g, "\\$&");
    }
  }
  return new RegExp(`${source}$`, "i");
}

/**
 * Match a filename pattern against a file: a pattern containing `/` is tested
 * against the whole (slash-normalized) path, otherwise against the basename.
 */
export function matchesFilenamePattern(pattern: string, path: string, filename: string) {
  const normalizedPattern = pattern.replace(/\\/g, "/");
  const target = normalizedPattern.includes("/") ? path : filename;
  return globToRegExp(normalizedPattern).test(target);
}
