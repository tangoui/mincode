import { readdir, readFile, stat } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const projectDir = fileURLToPath(new URL("../", import.meta.url));
const distDir = pathToFileURL(resolve(projectDir, process.env.VUE_OUTDIR ?? "../frontend/dist") + sep);
const assetsDir = new URL("./assets/", distDir);
const kib = 1024;
const budgets = {
  entryJavaScript: 192 * kib,
  entryCss: 400 * kib,
  largestApplicationChunk: 500 * kib,
  monacoEditorApi: 2_800 * kib,
  monacoReferenceSearch: 1_200 * kib,
};

async function sizeOf(relativePath) {
  return (await stat(new URL(relativePath, distDir))).size;
}

const html = await readFile(new URL("index.html", distDir), "utf8");
const entryScript = html.match(/<script[^>]+src="\.\/(assets\/index-[^"]+\.js)"/)?.[1]
  ?? html.match(/<script[^>]+src="\/(assets\/index-[^"]+\.js)"/)?.[1];
const entryStyle = html.match(/<link[^>]+href="\.\/(assets\/index-[^"]+\.css)"/)?.[1]
  ?? html.match(/<link[^>]+href="\/(assets\/index-[^"]+\.css)"/)?.[1];
if (!entryScript || !entryStyle) throw new Error(`Unable to locate entry assets in ${fileURLToPath(new URL("index.html", distDir))}`);

const files = await readdir(assetsDir);
const failures = [];
const checks = [
  ["entry JavaScript", await sizeOf(entryScript), budgets.entryJavaScript],
  ["entry CSS", await sizeOf(entryStyle), budgets.entryCss],
];

const monacoBudgetedChunks = [
  [["editor.api-"], "Monaco editor API", budgets.monacoEditorApi],
  [
    ["standaloneReferenceSearch-", "wordPartOperations-"],
    "Monaco reference search",
    budgets.monacoReferenceSearch,
  ],
];
const monacoPrefixes = monacoBudgetedChunks.flatMap(([prefixes]) => prefixes);
const applicationChunks = await Promise.all(
  files
    .filter(
      (file) =>
        file.endsWith(".js") &&
        !file.includes("worker") &&
        !monacoPrefixes.some((prefix) => file.startsWith(prefix)),
    )
    .map(async (file) => ({ file, size: (await stat(join(fileURLToPath(assetsDir), file))).size })),
);
const largestApplicationChunk = applicationChunks.sort((a, b) => b.size - a.size)[0];
if (!largestApplicationChunk) failures.push("No application JavaScript chunk found");
else {
  checks.push([
    `largest application chunk (${largestApplicationChunk.file})`,
    largestApplicationChunk.size,
    budgets.largestApplicationChunk,
  ]);
}

for (const [prefixes, label, limit] of monacoBudgetedChunks) {
  const file = files.find(
    (candidate) => prefixes.some((prefix) => candidate.startsWith(prefix)) && candidate.endsWith(".js"),
  );
  if (!file) failures.push(`${label} chunk is missing`);
  else checks.push([label, (await stat(join(fileURLToPath(assetsDir), file))).size, limit]);
}

for (const forbidden of ["css.worker-", "html.worker-", "ts.worker-", "toggleHighContrast-"]) {
  if (files.some((file) => file.startsWith(forbidden))) {
    failures.push(`forbidden eager Monaco asset found: ${forbidden}*`);
  }
}

for (const [label, actual, limit] of checks) {
  const message = `${label}: ${(actual / kib).toFixed(1)} KiB / ${(limit / kib).toFixed(1)} KiB`;
  if (actual > limit) failures.push(message);
  else console.log(`✓ ${message}`);
}

if (failures.length) {
  console.error("Bundle budget exceeded:\n" + failures.map((failure) => `- ${failure}`).join("\n"));
  process.exitCode = 1;
}
