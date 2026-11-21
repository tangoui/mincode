import { ref } from "vue";

const pendingProjectFiles = ref<string[]>([]);
const pendingDatabaseFiles = ref<string[]>([]);

function appendUnique(target: typeof pendingProjectFiles, files: string[]) {
  const next = [...target.value];
  for (const file of files.map((value) => value.trim()).filter(Boolean)) {
    if (!next.includes(file)) next.push(file);
  }
  target.value = next;
}

export function enqueueProjectFiles(files: string[]) {
  appendUnique(pendingProjectFiles, files);
}

export function enqueueDatabaseFiles(files: string[]) {
  appendUnique(pendingDatabaseFiles, files);
}

export function consumeProjectFiles() {
  const files = [...pendingProjectFiles.value];
  pendingProjectFiles.value = [];
  return files;
}

export function consumeDatabaseFiles() {
  const files = [...pendingDatabaseFiles.value];
  pendingDatabaseFiles.value = [];
  return files;
}

export { pendingProjectFiles, pendingDatabaseFiles };
