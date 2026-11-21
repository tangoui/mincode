/**
 * Calls a plugin language feature provider through the host
 * (`plugin_host_language_feature`). The host checks the provider declares the
 * feature and language, bounds text, payload, time and result, and forwards
 * the relative path only to plugins with workspace.read; the responses are
 * re-validated here. Each kind sends only its own fields (the host rejects
 * the others): position for hover / completion / definition / references,
 * triggerCharacter for completion, includeDeclaration for references, range
 * for inlayHints; codeLens and documentSymbol take the whole document
 * (startLine 1), and codeAction the whole document with the selected range
 * and its diagnostics. projectRoot (host-only, never in the plugin payload) goes
 * with definition and references when relativePath is known and the host
 * would accept the root (isHostProjectRoot); otherwise it is left out so the
 * request still answers with same-document locations.
 */
import {
  isHostProjectRoot,
  parseCodeLensResponse,
  parseDefinitionResponse,
  parseDocumentSymbolResponse,
  parseInlayHintsResponse,
  parseReferencesResponse,
  type CodeLensResponse,
  type DocumentSymbolResponse,
  type InlayHintsResponse,
  type LocationsResponse,
} from "@/domain/plugins/language-feature-results";
import {
  parseCompletionResponse,
  parseHoverResponse,
  parseCodeActionResponse,
  type CodeActionResponse,
  type CompletionResponse,
  type HoverResponse,
  type LanguageFeatureDiagnostic,
  type LanguageFeatureKind,
  type PluginLanguageFeature,
  type PluginRange,
} from "@/domain/plugins/language-features";
import { invokeProviderCommand, type ProviderInvokeResult } from "@/services/plugin-provider-invoke";

export interface LanguageFeatureRequest {
  languageId: string;
  fileName: string;
  relativePath?: string;
  /** Absolute root of the document's project (definition / references, with relativePath). */
  projectRoot?: string;
  text: string;
  startLine: number;
  position?: { line: number; column: number };
  /** inlayHints: the visible lines, inside the sent text. */
  range?: { startLine: number; endLine: number };
  /** references only. */
  includeDeclaration?: boolean;
  /** completion only. */
  triggerCharacter?: string;
  locale: string;
}

/** A codeAction request: the whole document (startLine 1), the selected range and its diagnostics. */
export type CodeActionRequest = Pick<LanguageFeatureRequest, "languageId" | "fileName" | "relativePath" | "text" | "startLine" | "locale"> & {
  range: PluginRange;
  diagnostics: LanguageFeatureDiagnostic[];
};

type ProviderRef = Pick<PluginLanguageFeature, "pluginId" | "id">;

/** The host arguments of a request: the common fields plus the kind's own fields. */
function requestArgs(provider: ProviderRef, feature: LanguageFeatureKind, request: LanguageFeatureRequest): Record<string, unknown> {
  const args: Record<string, unknown> = {
    pluginId: provider.pluginId,
    providerId: provider.id,
    feature,
    languageId: request.languageId,
    fileName: request.fileName,
  };
  if (request.relativePath !== undefined) args.relativePath = request.relativePath;
  args.text = request.text;
  args.startLine = request.startLine;
  switch (feature) {
    case "hover":
      args.position = request.position;
      break;
    case "completion":
      args.position = request.position;
      if (request.triggerCharacter !== undefined) args.triggerCharacter = request.triggerCharacter;
      break;
    case "definition":
    case "references":
      args.position = request.position;
      if (feature === "references") args.includeDeclaration = request.includeDeclaration === true;
      if (request.projectRoot && request.relativePath && isHostProjectRoot(request.projectRoot)) args.projectRoot = request.projectRoot;
      break;
    case "inlayHints":
      args.range = request.range;
      break;
    case "codeLens":
    case "documentSymbol":
      break;
  }
  args.locale = request.locale;
  return args;
}

function invoke<T>(
  provider: ProviderRef,
  feature: LanguageFeatureKind,
  request: LanguageFeatureRequest,
  parse: (value: unknown) => T,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<T>> {
  return invokeProviderCommand("plugin_host_language_feature", requestArgs(provider, feature, request), parse, signal);
}

/** codeLens / documentSymbol only take the whole document; a window never reaches the host. */
function wholeDocument<T>(
  provider: ProviderRef,
  feature: "codeLens" | "documentSymbol",
  request: LanguageFeatureRequest,
  parse: (value: unknown) => T,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<T>> {
  if (request.startLine !== 1) {
    return Promise.resolve({ ok: false, error: { code: "invalid_request", detail: `${feature} needs the whole document` } });
  }
  return invoke(provider, feature, request, parse, signal);
}

export function requestHover(provider: ProviderRef, request: LanguageFeatureRequest, signal?: AbortSignal): Promise<ProviderInvokeResult<HoverResponse>> {
  return invoke(provider, "hover", request, parseHoverResponse, signal);
}

export function requestCompletion(
  provider: ProviderRef,
  request: LanguageFeatureRequest,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<CompletionResponse>> {
  return invoke(provider, "completion", request, parseCompletionResponse, signal);
}

export function requestDefinition(
  provider: ProviderRef,
  request: LanguageFeatureRequest,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<LocationsResponse>> {
  return invoke(provider, "definition", request, parseDefinitionResponse, signal);
}

export function requestReferences(
  provider: ProviderRef,
  request: LanguageFeatureRequest & { includeDeclaration: boolean },
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<LocationsResponse>> {
  return invoke(provider, "references", request, parseReferencesResponse, signal);
}

export function requestCodeLens(
  provider: ProviderRef,
  request: LanguageFeatureRequest,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<CodeLensResponse>> {
  return wholeDocument(provider, "codeLens", request, parseCodeLensResponse, signal);
}

/** The response is re-validated against the request range (hints on other lines are dropped). */
export function requestInlayHints(
  provider: ProviderRef,
  request: LanguageFeatureRequest & { range: { startLine: number; endLine: number } },
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<InlayHintsResponse>> {
  const range = request.range;
  return invoke(provider, "inlayHints", request, (value) => parseInlayHintsResponse(value, range), signal);
}

export function requestDocumentSymbols(
  provider: ProviderRef,
  request: LanguageFeatureRequest,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<DocumentSymbolResponse>> {
  return wholeDocument(provider, "documentSymbol", request, parseDocumentSymbolResponse, signal);
}

/** Code actions only take the whole document; their edits are re-checked against the model. */
export function requestCodeAction(
  provider: ProviderRef,
  request: CodeActionRequest,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<CodeActionResponse>> {
  if (request.startLine !== 1) {
    return Promise.resolve({ ok: false, error: { code: "invalid_request", detail: "codeAction needs the whole document" } });
  }
  const args: Record<string, unknown> = {
    pluginId: provider.pluginId,
    providerId: provider.id,
    feature: "codeAction",
    languageId: request.languageId,
    fileName: request.fileName,
  };
  if (request.relativePath !== undefined) args.relativePath = request.relativePath;
  args.text = request.text;
  args.startLine = request.startLine;
  args.range = request.range;
  args.diagnostics = request.diagnostics;
  args.locale = request.locale;
  return invokeProviderCommand("plugin_host_language_feature", args, parseCodeActionResponse, signal);
}
