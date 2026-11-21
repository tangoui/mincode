import { matchFramework, type EditorFramework, type FrameworkRoot } from "./frameworks";

type Registration = { pluginId: string; framework: EditorFramework };
type Reader = (projectRoot: string, relativePath: string) => Promise<string | undefined>;

/** 不扫描整棵目录，只检查打开文档的祖先；读取由调用者经原生安全边界完成。 */
export async function detectFrameworkRoots(registrations: readonly Registration[], projects: readonly string[], paths: readonly string[], read: Reader): Promise<{ roots: FrameworkRoot[]; boundaries: string[] }> {
  const roots: FrameworkRoot[] = [];
  const boundaries = new Set(projects);
  const candidates = new Map<string, { project: string; relative: string }>();
  const projectRoots = projects.slice(0, 12).map((project) => ({ project, root: project.replace(/\\/g, "/").replace(/\/+$/, "") }));
  for (const { project, root } of projectRoots) candidates.set(root, { project, relative: "" });
  for (const { project, root } of projectRoots) {
    const windows = /^[A-Za-z]:\//.test(root);
    for (const path of paths.slice(0, 100)) {
      if (candidates.size >= 128) break;
      const file = path.replace(/\\/g, "/");
      if (!(windows ? file.toLowerCase().startsWith(`${root.toLowerCase()}/`) : file.startsWith(`${root}/`))) continue;
      const segments = file.slice(root.length + 1).split("/").slice(0, -1);
      for (let count = 1; count <= Math.min(segments.length, 16); count += 1) {
        if (candidates.size >= 128) break;
        const relative = segments.slice(0, count).join("/");
        candidates.set(`${root}/${relative}`, { project, relative });
      }
    }
  }
  for (const [root, { project, relative }] of candidates) {
    const prefix = relative ? `${relative}/` : "";
    const files = new Map<string, string>();
    const names = new Set(["package.json", ...registrations.flatMap(({ framework }) => [...framework.match.all, ...framework.match.any, ...framework.match.exclude].map((condition) => condition.file))]);
    for (const name of names) {
      const text = await read(project, `${prefix}${name}`);
      if (text !== undefined && text.length <= 262144) files.set(name, text);
    }
    if (files.has("package.json")) boundaries.add(root);
    for (const { pluginId, framework } of registrations) {
      if (matchFramework(framework, files)) roots.push({ root, pluginId, framework });
    }
  }
  return { roots, boundaries: [...boundaries] };
}
