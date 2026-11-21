import { matchesFilenamePattern } from "../files/filename-glob.ts";

export interface ProjectEditorLanguageDefinition {
  id: string;
  extensions?: readonly string[];
  filenames?: readonly string[];
  filenamePatterns?: readonly string[];
  firstLine?: string;
}

const LANGUAGE_BY_EXTRA_EXTENSION: Readonly<Record<string, string>> = {
  cfg: "ini",
  conf: "ini",
  fish: "shell",
  json5: "json",
  jsonc: "json",
  sass: "scss",
  zsh: "shell",
};

const LANGUAGE_BY_EXTRA_FILENAME: Readonly<Record<string, string>> = {
  ".bash_profile": "shell",
  ".bashrc": "shell",
  ".env": "ini",
  ".npmrc": "ini",
  ".profile": "shell",
  ".zshrc": "shell",
  containerfile: "dockerfile",
};

/**
 * Resolve a project file path using Monaco's registered language metadata.
 * Project-specific aliases are intentionally small; Monaco remains the source of truth.
 */
export function detectProjectEditorLanguage(
  path: string,
  definitions: readonly ProjectEditorLanguageDefinition[],
  firstLine = ""
) {
  const normalizedPath = path.replace(/\\/g, "/").toLowerCase();
  const filename = normalizedPath.split("/").pop() ?? "";

  const exactOverride = LANGUAGE_BY_EXTRA_FILENAME[filename];
  if (exactOverride) return exactOverride;
  if (filename.startsWith(".env.")) return "ini";
  if (filename.startsWith("dockerfile.") || filename.startsWith("containerfile.")) {
    return "dockerfile";
  }

  const separator = filename.lastIndexOf(".");
  const extension = separator >= 0 ? filename.slice(separator + 1) : "";
  const extensionOverride = LANGUAGE_BY_EXTRA_EXTENSION[extension];
  if (extensionOverride) return extensionOverride;

  for (let index = definitions.length - 1; index >= 0; index -= 1) {
    const definition = definitions[index];
    if (definition.filenames?.some((candidate) => candidate.toLowerCase() === filename)) {
      return definition.id;
    }
  }

  let patternMatch: { id: string; length: number } | undefined;
  let extensionMatch: { id: string; length: number } | undefined;
  for (let index = definitions.length - 1; index >= 0; index -= 1) {
    const definition = definitions[index];
    for (const pattern of definition.filenamePatterns ?? []) {
      if (
        (!patternMatch || pattern.length > patternMatch.length) &&
        matchesFilenamePattern(pattern, normalizedPath, filename)
      ) {
        patternMatch = { id: definition.id, length: pattern.length };
      }
    }
    for (const candidate of definition.extensions ?? []) {
      const normalizedExtension = candidate.toLowerCase();
      if (
        (!extensionMatch || normalizedExtension.length > extensionMatch.length) &&
        filename.endsWith(normalizedExtension)
      ) {
        extensionMatch = { id: definition.id, length: normalizedExtension.length };
      }
    }
  }
  if (patternMatch) return patternMatch.id;
  if (extensionMatch) return extensionMatch.id;

  if (firstLine) {
    const normalizedFirstLine = firstLine.startsWith("\uFEFF") ? firstLine.slice(1) : firstLine;
    for (let index = definitions.length - 1; index >= 0; index -= 1) {
      const definition = definitions[index];
      if (!definition.firstLine) continue;
      const source = definition.firstLine.startsWith("^")
        ? definition.firstLine
        : `^${definition.firstLine}`;
      try {
        if (new RegExp(source).test(normalizedFirstLine)) return definition.id;
      } catch {
        // Ignore malformed third-party registration metadata and continue to plaintext.
      }
    }
  }

  return "plaintext";
}

/** `*.<segment>.<segment>[...]`: a literal suffix of two or more dot segments, no other wildcards. */
const COMPOUND_SUFFIX_PATTERN = /^\*((?:\.[a-z0-9_+-]+){2,})$/i;

/**
 * Language of the longest plugin compound suffix pattern (`*.blade.php`) that
 * matches `path` and is strictly longer than every built-in extension the file
 * also ends with. This lets a plugin claim a more specific kind of file inside
 * a built-in extension without ever taking over the extension itself
 * (`*.php`, `**\/views/*.php` and wildcard suffixes never qualify). A built-in
 * exact filename, a filename pattern, or an extension at least as long wins.
 */
export function detectCompoundSuffixLanguage(
  path: string,
  plugins: readonly ProjectEditorLanguageDefinition[],
  builtIns: readonly ProjectEditorLanguageDefinition[]
): string | undefined {
  const normalizedPath = path.replace(/\\/g, "/").toLowerCase();
  const filename = normalizedPath.split("/").pop() ?? "";
  let best: { id: string; length: number } | undefined;
  for (const definition of plugins) {
    for (const pattern of definition.filenamePatterns ?? []) {
      const suffix = COMPOUND_SUFFIX_PATTERN.exec(pattern)?.[1]?.toLowerCase();
      if (!suffix || filename.length <= suffix.length || !filename.endsWith(suffix)) continue;
      if (!best || suffix.length > best.length) best = { id: definition.id, length: suffix.length };
    }
  }
  if (!best) return undefined;
  const length = best.length;
  const builtInClaims = builtIns.some((definition) =>
    definition.filenames?.some((candidate) => candidate.toLowerCase() === filename) ||
    definition.filenamePatterns?.some((pattern) => matchesFilenamePattern(pattern, normalizedPath, filename)) ||
    definition.extensions?.some((extension) => extension.length >= length && filename.endsWith(extension.toLowerCase()))
  );
  return builtInClaims ? undefined : best.id;
}
