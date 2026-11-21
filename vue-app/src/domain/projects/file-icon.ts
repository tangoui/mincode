export type ProjectFileIconKind =
  | "folder"
  | "vue"
  | "typescript"
  | "javascript"
  | "markup"
  | "style"
  | "data"
  | "config"
  | "markdown"
  | "image"
  | "video"
  | "audio"
  | "database"
  | "archive"
  | "terminal"
  | "code"
  | "document";

const EXTENSION_KIND: Readonly<Record<string, ProjectFileIconKind>> = {
  vue: "vue",
  ts: "typescript",
  tsx: "typescript",
  mts: "typescript",
  cts: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  html: "markup",
  htm: "markup",
  xml: "markup",
  xhtml: "markup",
  css: "style",
  scss: "style",
  sass: "style",
  less: "style",
  styl: "style",
  json: "data",
  jsonc: "data",
  yaml: "data",
  yml: "data",
  toml: "data",
  csv: "data",
  tsv: "data",
  md: "markdown",
  mdx: "markdown",
  markdown: "markdown",
  mdwn: "markdown",
  png: "image",
  jpg: "image",
  jpeg: "image",
  jfif: "image",
  gif: "image",
  webp: "image",
  bmp: "image",
  ico: "image",
  avif: "image",
  tif: "image",
  tiff: "image",
  svg: "image",
  mp4: "video",
  mov: "video",
  avi: "video",
  mkv: "video",
  webm: "video",
  m4v: "video",
  mp3: "audio",
  wav: "audio",
  flac: "audio",
  aac: "audio",
  ogg: "audio",
  m4a: "audio",
  sql: "database",
  db: "database",
  sqlite: "database",
  sqlite3: "database",
  duckdb: "database",
  mdb: "database",
  zip: "archive",
  rar: "archive",
  "7z": "archive",
  tar: "archive",
  gz: "archive",
  bz2: "archive",
  xz: "archive",
  tgz: "archive",
  jar: "archive",
  war: "archive",
  sh: "terminal",
  bash: "terminal",
  zsh: "terminal",
  fish: "terminal",
  ps1: "terminal",
  bat: "terminal",
  cmd: "terminal",
  go: "code",
  rs: "code",
  py: "code",
  pyw: "code",
  java: "code",
  kt: "code",
  kts: "code",
  c: "code",
  h: "code",
  cc: "code",
  cpp: "code",
  cxx: "code",
  hpp: "code",
  cs: "code",
  php: "code",
  rb: "code",
  swift: "code",
  scala: "code",
  dart: "code",
  lua: "code",
  r: "code",
  ex: "code",
  exs: "code",
  erl: "code",
  fs: "code",
  fsx: "code",
  clj: "code",
  cljs: "code",
  groovy: "code",
  sol: "code",
  ini: "config",
  conf: "config",
  config: "config",
  properties: "config",
  lock: "config",
};

const CONFIG_FILE_NAMES = new Set([
  ".babelrc",
  ".browserslistrc",
  ".dockerignore",
  ".editorconfig",
  ".eslintignore",
  ".eslintrc",
  ".gitattributes",
  ".gitignore",
  ".gitmodules",
  ".npmrc",
  ".prettierignore",
  ".prettierrc",
  ".stylelintrc",
  "biome.json",
  "deno.json",
  "jsconfig.json",
  "tsconfig.json",
]);

const PACKAGE_FILE_NAMES = new Set([
  "bun.lock",
  "bun.lockb",
  "cargo.lock",
  "cargo.toml",
  "composer.json",
  "composer.lock",
  "go.mod",
  "go.sum",
  "package-lock.json",
  "package.json",
  "pnpm-lock.yaml",
  "pyproject.toml",
  "requirements.txt",
  "yarn.lock",
]);

const TERMINAL_FILE_NAMES = new Set([
  "containerfile",
  "dockerfile",
  "gemfile",
  "justfile",
  "makefile",
  "procfile",
  "rakefile",
]);

export function resolveProjectFileIconKind(name: string, isDir = false): ProjectFileIconKind {
  if (isDir) return "folder";

  const normalizedName = name.replace(/\\/g, "/").split("/").pop()?.toLowerCase() ?? "";
  if (!normalizedName) return "document";
  if (normalizedName === ".env" || normalizedName.startsWith(".env.")) return "config";
  if (CONFIG_FILE_NAMES.has(normalizedName)) return "config";
  if (PACKAGE_FILE_NAMES.has(normalizedName)) return "archive";
  if (
    TERMINAL_FILE_NAMES.has(normalizedName) ||
    normalizedName.startsWith("dockerfile.") ||
    normalizedName.startsWith("containerfile.")
  ) {
    return "terminal";
  }

  const extensionIndex = normalizedName.lastIndexOf(".");
  const extension = extensionIndex >= 0 ? normalizedName.slice(extensionIndex + 1) : "";
  return EXTENSION_KIND[extension] ?? "document";
}
