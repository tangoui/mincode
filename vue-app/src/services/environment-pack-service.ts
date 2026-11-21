import {
  chooseNativeFile,
  chooseNativeSaveFile,
  invokeBackend,
  isDesktopRuntime,
} from "@/services/desktop-bridge";

export interface SelectedEnvironmentPackFile {
  name: string;
  content: string;
}

const filters = [{ DisplayName: "MinCode Environment Pack", Pattern: "*.mincode" }];
const maxBrowserBytes = 5 * 1024 * 1024;

function chooseBrowserEnvironmentPack(): Promise<SelectedEnvironmentPackFile | undefined> {
  return new Promise((resolve, reject) => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".mincode,application/octet-stream,text/plain";
    input.addEventListener(
      "change",
      async () => {
        const file = input.files?.[0];
        if (!file) return resolve(undefined);
        if (file.size > maxBrowserBytes) return reject(new Error("Environment pack exceeds 5 MB"));
        try {
          resolve({ name: file.name, content: await file.text() });
        } catch (error) {
          reject(error);
        }
      },
      { once: true }
    );
    input.click();
  });
}

export async function chooseEnvironmentPackFile(
  title: string
): Promise<SelectedEnvironmentPackFile | undefined> {
  if (!isDesktopRuntime()) return chooseBrowserEnvironmentPack();
  const path = await chooseNativeFile(title, filters);
  if (!path) return undefined;
  return {
    name: path.split(/[\\/]/).pop() || "environment.mincode",
    content: await invokeBackend<string>("local_fs_read_selected_text_file", { path }),
  };
}

export async function saveEnvironmentPackFile(
  title: string,
  filename: string,
  content: string
) {
  if (isDesktopRuntime()) {
    const path = await chooseNativeSaveFile(title, filename, filters);
    if (!path) return false;
    await invokeBackend<void>("local_fs_write_selected_text_file", { path, content });
    return true;
  }

  const url = URL.createObjectURL(new Blob([content], { type: "application/octet-stream" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
  return true;
}
