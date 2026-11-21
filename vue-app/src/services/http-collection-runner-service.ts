import {
  evaluateHttpCollectionResponse,
  summarizeHttpCollectionRun,
  type HttpCollectionRunResponse,
  type HttpCollectionRunResult,
  type HttpCollectionRunSummary,
} from "@/domain/http/collection-runner";
import { buildNativeHttpRequestPayload } from "@/domain/http/native-request";
import {
  resolveHttpRequestEnvironment,
  type HttpRequestCollection,
  type HttpRequestDraft,
  type SavedHttpRequest,
} from "@/domain/http/request-collection";
import { invokeBackend, isDesktopRuntime } from "./desktop-bridge";

export interface HttpCollectionRunOptions {
  shouldContinue?: () => boolean;
  onResult?: (result: HttpCollectionRunResult, index: number) => void;
}

function createRequestExpander(collection: HttpRequestCollection, request: SavedHttpRequest) {
  const variables = Object.fromEntries(
    (resolveHttpRequestEnvironment(
      collection,
      request.environmentId,
      request.environmentMode
    )?.vars ?? [])
      .filter((row) => row.enabled && row.key.trim())
      .map((row) => [row.key.trim(), row.value])
  );
  return (value: string) =>
    value.replace(/\{\{\s*([^{}\s]+)\s*\}\}/g, (match, key: string) => variables[key] ?? match);
}

export async function runHttpCollection(
  collection: HttpRequestCollection,
  options: HttpCollectionRunOptions = {}
): Promise<HttpCollectionRunSummary> {
  if (!isDesktopRuntime()) throw new Error("desktop_required");
  const startedAt = performance.now();
  const results: HttpCollectionRunResult[] = [];

  for (let index = 0; index < collection.requests.length; index += 1) {
    if (options.shouldContinue && !options.shouldContinue()) break;
    const request = collection.requests[index]!;
    try {
      const response = await invokeBackend<HttpCollectionRunResponse>("http_send_request", {
        payload: buildNativeHttpRequestPayload(
          { ...request, authToken: request.authToken ?? "" } as HttpRequestDraft,
          createRequestExpander(collection, request)
        ),
      });
      const result = evaluateHttpCollectionResponse(request, response);
      results.push(result);
      options.onResult?.(result, index);
    } catch (error) {
      const result: HttpCollectionRunResult = {
        requestId: request.id,
        requestName: request.name,
        method: request.method,
        url: request.url,
        passed: false,
        elapsedMs: 0,
        error: String(error),
      };
      results.push(result);
      options.onResult?.(result, index);
    }
  }

  return summarizeHttpCollectionRun(results, Math.round(performance.now() - startedAt));
}
