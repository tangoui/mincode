export interface MarkdownLocalProject {
  root: string;
}

export interface LocalMarkdownTarget {
  projectRoot: string;
  relativePath: string;
  absolutePath: string;
}

const WINDOWS_ABSOLUTE_PATH = /^[A-Za-z]:[\\/]/;
const SYSTEM_PACKAGE_EXTENSIONS = new Set(["app", "bundle", "framework", "plugin"]);

function normalizeSlashes(path: string) {
  return path.replace(/\\/g, "/");
}

function trimTrailingSlash(path: string) {
  if (path === "/" || /^[A-Za-z]:\/$/.test(path)) return path;
  return path.replace(/\/+$/, "");
}

function decodeLocalPath(path: string) {
  const trimmed = path.trim();
  if (!trimmed.toLowerCase().startsWith("file:")) {
    try {
      return decodeURIComponent(trimmed);
    } catch {
      return trimmed;
    }
  }

  try {
    const url = new URL(trimmed);
    let pathname = decodeURIComponent(url.pathname);
    if (/^\/[A-Za-z]:\//.test(pathname)) pathname = pathname.slice(1);
    return pathname;
  } catch {
    return "";
  }
}

function isAbsolutePath(path: string) {
  return path.startsWith("/") || WINDOWS_ABSOLUTE_PATH.test(path);
}

function isPathInsideRoot(path: string, root: string) {
  const normalizedPath = trimTrailingSlash(normalizeSlashes(path));
  const normalizedRoot = trimTrailingSlash(normalizeSlashes(root));
  const caseInsensitive = WINDOWS_ABSOLUTE_PATH.test(normalizedRoot);
  const candidate = caseInsensitive ? normalizedPath.toLowerCase() : normalizedPath;
  const parent = caseInsensitive ? normalizedRoot.toLowerCase() : normalizedRoot;
  return candidate === parent || candidate.startsWith(`${parent}/`);
}

function relativeToRoot(path: string, root: string) {
  const normalizedPath = trimTrailingSlash(normalizeSlashes(path));
  const normalizedRoot = trimTrailingSlash(normalizeSlashes(root));
  return normalizedPath === normalizedRoot ? "" : normalizedPath.slice(normalizedRoot.length + 1);
}

function normalizeRelativePath(path: string) {
  const segments: string[] = [];
  for (const segment of normalizeSlashes(path).split("/")) {
    if (!segment || segment === ".") continue;
    if (segment === "..") {
      if (!segments.length) return null;
      segments.pop();
      continue;
    }
    segments.push(segment);
  }
  return segments.join("/");
}

/**
 * 只允许 Markdown 本地链接落在已保存项目中。绝对路径匹配最长项目根，
 * 相对路径基于当前项目，并在调用原生文件 API 前先阻止越界。
 */
export function resolveLocalMarkdownTarget(
  rawPath: string,
  projects: readonly MarkdownLocalProject[],
  activeProjectRoot: string,
): LocalMarkdownTarget | null {
  const decoded = decodeLocalPath(rawPath);
  if (!decoded) return null;
  const segments = normalizeSlashes(decoded).split("/");
  if (segments.includes("..")) return null;

  if (isAbsolutePath(decoded)) {
    const project = [...projects]
      .filter((entry) => isPathInsideRoot(decoded, entry.root))
      .sort((left, right) => right.root.length - left.root.length)[0];
    if (!project) return null;
    const relativePath = relativeToRoot(decoded, project.root);
    return {
      projectRoot: project.root,
      relativePath,
      absolutePath: trimTrailingSlash(normalizeSlashes(decoded)),
    };
  }

  const activeProject = projects.find((entry) => entry.root === activeProjectRoot);
  if (!activeProject) return null;
  const relativePath = normalizeRelativePath(decoded);
  if (relativePath === null) return null;
  const projectRoot = trimTrailingSlash(normalizeSlashes(activeProject.root));
  return {
    projectRoot: activeProject.root,
    relativePath,
    absolutePath: relativePath ? `${projectRoot}/${relativePath}` : projectRoot,
  };
}

/** macOS 等平台把部分目录后缀作为单个文件包展示，定位时必须强制选中而不是打开。 */
export function isSystemPackagePath(path: string) {
  const name = trimTrailingSlash(normalizeSlashes(path)).split("/").pop() ?? "";
  const extension = name.split(".").pop()?.toLowerCase();
  return Boolean(extension && SYSTEM_PACKAGE_EXTENSIONS.has(extension));
}
