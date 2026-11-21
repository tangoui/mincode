import {
  chooseNativeFile,
  chooseNativeSaveFile,
  invokeBackend,
  isDesktopRuntime,
} from "@/services/desktop-bridge";
import type { ApiDocumentFormat } from "@/domain/http/api-document";

export interface ImportedApiFile {
  name: string;
  content: string;
}

const API_FILE_FILTERS = [
  { DisplayName: "OpenAPI / Swagger / Postman / Apifox / Apipost", Pattern: "*.json;*.yaml;*.yml" },
];

function chooseBrowserFile(): Promise<ImportedApiFile | undefined> {
  return new Promise((resolve, reject) => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".json,.yaml,.yml,application/json,application/yaml,text/yaml";
    input.addEventListener("change", async () => {
      const file = input.files?.[0];
      if (!file) return resolve(undefined);
      try {
        resolve({ name: file.name, content: await file.text() });
      } catch (error) {
        reject(error);
      }
    }, { once: true });
    input.click();
  });
}

export async function chooseApiDocumentFile(title: string): Promise<ImportedApiFile | undefined> {
  if (!isDesktopRuntime()) return chooseBrowserFile();
  const path = await chooseNativeFile(title, API_FILE_FILTERS);
  if (!path) return undefined;
  return {
    name: path.split(/[\\/]/).pop() || "api-document.json",
    content: await invokeBackend<string>("http_api_document_read", { path }),
  };
}

function exportExtension(format: ApiDocumentFormat) {
  if (format === "openapi-yaml") return "yaml";
  return "json";
}

function safeFilename(value: string) {
  return value.trim().replace(/[\\/:*?"<>|]+/g, "-").replace(/\s+/g, "-") || "api-collection";
}

export async function saveApiDocumentFile(
  title: string,
  collectionName: string,
  format: ApiDocumentFormat,
  content: string
) {
  const suffix = format === "postman" ? "postman" : "openapi";
  const filename = `${safeFilename(collectionName)}.${suffix}.${exportExtension(format)}`;
  if (isDesktopRuntime()) {
    const path = await chooseNativeSaveFile(title, filename, [{
      DisplayName: format === "openapi-yaml" ? "YAML" : "JSON",
      Pattern: format === "openapi-yaml" ? "*.yaml" : "*.json",
    }]);
    if (!path) return false;
    await invokeBackend<void>("http_api_document_write", { path, content });
    return true;
  }

  const blob = new Blob([content], {
    type: format === "openapi-yaml" ? "application/yaml;charset=utf-8" : "application/json;charset=utf-8",
  });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
  return true;
}
