/**
 * Runtime of the `jsonValidation` contribution point consumer, loaded on
 * demand by services/contributions/json-validation.ts only when a plugin
 * contributes JSON schemas. `collectJsonValidationEntries` re-validates the
 * snapshot, which carries asset keys only; the Monaco stage
 * (services/monaco-json-schemas.ts) fetches schemas lazily, probes their
 * regexes and registers them for the exact matching models with schema
 * requests disabled. Applying data never starts a plugin process and never
 * loads Monaco.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { collectJsonValidationEntries, type JsonValidationIssue } from "@/domain/plugins/json-validation";
import { monacoJsonSchemaIssues, setMonacoJsonSchemas } from "@/services/monaco-json-schemas";
import { useLocalProjectStore } from "@/stores/local-project";

let lastIssues: JsonValidationIssue[] = [];

/** Issues of the applied snapshot plus Monaco-stage issues, for diagnostics. */
export function jsonValidationRuntimeIssues(): readonly JsonValidationIssue[] {
  return [...lastIssues, ...monacoJsonSchemaIssues()];
}

function projectRoots(): string[] {
  try {
    return useLocalProjectStore().projects.map((project) => project.root);
  } catch {
    return [];
  }
}

/** Applies one snapshot of the point. Returns the cleanup of this snapshot. */
export function applyJsonValidationContributions(contributions: PointContribution[]): () => void {
  const result = collectJsonValidationEntries(contributions);
  lastIssues = result.issues;
  setMonacoJsonSchemas(result.entries, projectRoots);
  // Schema bytes stay in the shared, byte-bounded asset LRU: releasing a
  // plugin there would also revoke its other points' blob URLs (file icons).
  return () => {
    lastIssues = [];
    setMonacoJsonSchemas([]);
  };
}
