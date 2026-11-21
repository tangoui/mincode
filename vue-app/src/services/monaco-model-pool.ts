import * as monaco from "@/services/monaco-runtime.js";

interface ModelLease {
  model: monaco.editor.ITextModel;
  references: number;
  owned: boolean;
}

const leases = new Map<string, ModelLease>();

export function acquireMonacoModel(
  uri: monaco.Uri,
  create: () => monaco.editor.ITextModel
): monaco.editor.ITextModel {
  const key = uri.toString();
  const leased = leases.get(key);
  if (leased && !leased.model.isDisposed()) {
    leased.references += 1;
    return leased.model;
  }

  const existing = monaco.editor.getModel(uri);
  const model = existing ?? create();
  leases.set(key, {
    model,
    references: 1,
    owned: existing === null,
  });
  return model;
}

export function releaseMonacoModel(model: monaco.editor.ITextModel): void {
  const key = model.uri.toString();
  const leased = leases.get(key);
  if (!leased || leased.model !== model) return;

  leased.references -= 1;
  if (leased.references > 0) return;
  leases.delete(key);
  if (leased.owned && !model.isDisposed()) model.dispose();
}
