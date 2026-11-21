import { ref } from "vue";

const pendingDatabaseFiles = ref<string[]>([]);

function appendUnique(target: typeof pendingDatabaseFiles, files: string[]) {
  const next = [...target.value];
  for (const file of files.map((value) => value.trim()).filter(Boolean)) {
    if (!next.includes(file)) next.push(file);
  }
  target.value = next;
}

export function enqueueDatabaseFiles(files: string[]) {
  appendUnique(pendingDatabaseFiles, files);
}

export function consumeDatabaseFiles() {
  const files = [...pendingDatabaseFiles.value];
  pendingDatabaseFiles.value = [];
  return files;
}

export { pendingDatabaseFiles };
