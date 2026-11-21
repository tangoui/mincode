import { detectProjectEditorLanguage } from "@/domain/projects/editor-language";
import type { MonacoApi } from "@/services/monaco-contributions";

export function detectMonacoEditorLanguage(monaco: MonacoApi, path: string, value: string): string {
  const firstLine = value.split(/\r?\n/, 1)[0] ?? "";
  return detectProjectEditorLanguage(path, monaco.languages.getLanguages(), firstLine);
}
