/**
 * Loads plugin HTTP code generator templates (`httpCodeGenerators`).
 *
 * The template is fetched from the plugin host by content hash and
 * re-verified here (sha256 of the bytes must equal the key, strict UTF-8,
 * size cap). It is then parsed and rendered in the WebView against the
 * redacted request model (domain/plugins/http-code-generators.ts): the
 * plugin process is never started and never sees request data.
 */
import {
  HTTP_CODE_GENERATORS_CONTRIBUTION_POINT,
  HTTP_CODE_TEMPLATE_MAX_BYTES,
  type PluginHttpCodeGenerator,
} from "@/domain/plugins/http-code-generators";
import { fetchContributionAssets } from "@/services/contribution-assets";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export type HttpCodeTemplateErrorReason = "desktop" | "unavailable" | "integrity";

export class HttpCodeTemplateSourceError extends Error {
  readonly reason: HttpCodeTemplateErrorReason;

  constructor(reason: HttpCodeTemplateErrorReason, message: string) {
    super(message);
    this.reason = reason;
  }
}

function hex(buffer: ArrayBuffer) {
  return [...new Uint8Array(buffer)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** Fetches and verifies a generator's template; resolves to its text. */
export async function loadHttpCodeTemplate(generator: PluginHttpCodeGenerator): Promise<string> {
  if (!isDesktopRuntime()) throw new HttpCodeTemplateSourceError("desktop", "plugin code generators need the desktop app");
  const result = await fetchContributionAssets(generator.pluginId, HTTP_CODE_GENERATORS_CONTRIBUTION_POINT, [generator.template]);
  const asset = result.assets.get(generator.template);
  if (!asset || result.missing.length || result.stale.length) {
    throw new HttpCodeTemplateSourceError("unavailable", "the template changed or is unavailable; reload the plugin");
  }
  if (asset.bytes.byteLength > HTTP_CODE_TEMPLATE_MAX_BYTES) {
    throw new HttpCodeTemplateSourceError("integrity", "the template is too large");
  }
  const digest = hex(await crypto.subtle.digest("SHA-256", asset.bytes.slice().buffer as ArrayBuffer));
  if (digest !== generator.template) throw new HttpCodeTemplateSourceError("integrity", "the template failed its integrity check");
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(asset.bytes);
  } catch {
    throw new HttpCodeTemplateSourceError("integrity", "the template is not valid UTF-8");
  }
}
