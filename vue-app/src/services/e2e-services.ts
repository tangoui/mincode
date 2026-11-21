// Loaded only by the native -tags e2e smoke runner. Installs the packaged
// crypto-toolkit, image-toolkit and (when packaged) ai-translator, ocr and
// invoice-manager plugins and
// drives plugin services through the real backend commands the desktop UI uses
// (plugin_services_list/start/status/output_save/declared). Each check is
// reported on its own as "PASS <id>" or "FAIL <id>"; the scenario fails at the
// end when any check failed. Output files are saved into the harness's output
// directory; tools/e2e/desktop-smoke.mjs checks their bytes after the report.
import { invokeBackend, nativeErrorMessage } from "@/services/desktop-bridge";
import {
  beginPluginUISession, closePluginUISession, getPluginUITask, installLocalPlugin, invokePluginUI, previewLocalPlugin, setPluginEnabled,
  startPluginUITask, uninstallPlugin,
} from "@/services/plugin-service";
import { answerPrompt } from "@/services/plugin-host-ui";
import { usePluginHostUiStore } from "@/stores/plugin-host-ui";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import type { ServiceSurface } from "@/domain/plugins/services";

const CRYPTO = "crypto-toolkit";
const IMAGE = "image-toolkit";
const TRANSLATOR = "ai-translator";
const CONSUMER = "service-consumer-demo";
const OCR = "ocr";
const INVOICES = "invoice-manager";
// Key fields of the OCR plugin's synthetic invoice fixture (engine/testdata/invoice.txt).
const INVOICE_NUMBER = "26442000001234567890";
const INVOICE_TOTAL = "1,234.56";

interface ServiceEntry {
  pluginId: string;
  service: string;
  title: string;
  titleLocalized?: Record<string, string>;
  surfaces: string[];
  input: { kind: string };
  output: { kind: string };
  active?: boolean;
  inactiveReason?: string;
}

interface Snapshot {
  callId: string;
  state: "running" | "succeeded" | "failed" | "cancelled";
  output?: unknown;
  files?: { token: string; name: string; mime: string; size: number }[];
  error?: string;
}

type StartRequest = {
  service: string;
  surface: ServiceSurface;
  input?: string;
  paths?: string[];
  remoteFile?: { sessionId: string; path: string };
  content?: { text: string; contentType?: string };
  surfaceContext?: Record<string, unknown>;
  params?: Record<string, unknown>;
};

const delay = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

async function listServices(): Promise<ServiceEntry[]> {
  return (await invokeBackend<{ services: ServiceEntry[] }>("plugin_services_list")).services ?? [];
}

async function declaredServices(pluginId: string): Promise<ServiceEntry[]> {
  return (await invokeBackend<{ services: ServiceEntry[] }>("plugin_services_declared", { pluginId })).services ?? [];
}

/** Starts a host call and polls plugin_services_status until it settles. */
async function runService(request: StartRequest, timeoutMs: number): Promise<Snapshot> {
  let snapshot = await invokeBackend<Snapshot>("plugin_services_start", request);
  const deadline = performance.now() + timeoutMs;
  while (snapshot.state === "running") {
    if (performance.now() > deadline) throw new Error(`${request.service} still running after ${timeoutMs} ms`);
    await delay(100);
    snapshot = await invokeBackend<Snapshot>("plugin_services_status", { callId: snapshot.callId });
  }
  return snapshot;
}

function succeeded(snapshot: Snapshot, service: string): Snapshot {
  if (snapshot.state !== "succeeded") throw new Error(`${service} ended ${snapshot.state}: ${snapshot.error ?? "(no error)"}`);
  return snapshot;
}

/** Text output is a JSON string in the snapshot. */
function outputText(snapshot: Snapshot): string {
  if (typeof snapshot.output === "string") return snapshot.output;
  throw new Error(`text output is not a string: ${JSON.stringify(snapshot.output)?.slice(0, 200)}`);
}

async function sha256Hex(text: string) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

function base64Utf8(text: string) {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)));
}

async function install(packagePath: string, pluginId: string) {
  const preview = await previewLocalPlugin(packagePath);
  if (preview.manifest.id !== pluginId) throw new Error(`preview of ${packagePath} reported ${preview.manifest.id}`);
  await installLocalPlugin(packagePath);
  await setPluginEnabled(pluginId, true);
}

class Checks {
  readonly failures: string[] = [];
  constructor(private readonly log: { done(step: string): void }) {}

  async run(id: string, what: string, check: () => Promise<string | void>) {
    try {
      const detail = await check();
      this.log.done(`PASS ${id} ${what}${detail ? ` — ${detail}` : ""}`);
      return true;
    } catch (error) {
      const reason = nativeErrorMessage(error);
      this.failures.push(`${id}: ${reason}`);
      this.log.done(`FAIL ${id} ${what} — ${reason}`);
      return false;
    }
  }

  skip(id: string, what: string, reason: string) {
    this.log.done(`SKIP ${id} ${what} — ${reason}`);
  }
}

/**
 * Answers the host's next plugin-call authorization prompt for `caller` once,
 * the way the user would in PluginHostUiLayer (answerPrompt is what its
 * buttons call). Resolves with the prompt it answered.
 */
function approveNextPluginCall(caller: string, timeoutMs: number) {
  const store = usePluginHostUiStore();
  let stopped = false;
  const done = (async () => {
    const deadline = performance.now() + timeoutMs;
    while (!stopped && performance.now() < deadline) {
      const prompt = store.prompts.find((item) => item.kind === "pluginCall" && item.pluginId === caller);
      if (prompt) {
        await answerPrompt(prompt, { ids: ["once"] });
        return prompt;
      }
      await delay(50);
    }
    return undefined;
  })();
  return { done, stop: () => { stopped = true; } };
}

export async function servicesScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.services!;
  const timeout = config.timeoutMs;
  const checks = new Checks(log);
  const installed: string[] = [];
  try {
    for (const [pluginId, packagePath] of [[CRYPTO, fixture.packages.cryptoToolkit], [IMAGE, fixture.packages.imageToolkit]] as const) {
      await install(packagePath, pluginId);
      installed.push(pluginId);
    }
    if (fixture.packages.aiTranslator) {
      await checks.run("setup", "install ai-translator", async () => {
        await install(fixture.packages.aiTranslator!, TRANSLATOR);
        installed.push(TRANSLATOR);
      });
    }
    log.done(`installed and enabled: ${installed.join(", ")}`);

    // a. Listing, localized titles and default surfaces.
    await checks.run("a", "plugin_services_list", async () => {
      const services = await listServices();
      const expected: [string, string, string, string, string[]][] = [
        [CRYPTO, "text.base64-encode", "Base64 编码", "Base64 encode", ["palette", "selection"]],
        [CRYPTO, "text.hash", "计算摘要", "Hash text", ["palette", "selection"]],
        [CRYPTO, "text.jwt-decode", "解码 JWT", "Decode JWT", ["palette", "selection"]],
        [IMAGE, "image.convert", "转换图片格式", "Convert images", ["fileMenu", "palette"]],
        [IMAGE, "image.compress", "压缩图片", "Compress images", ["fileMenu", "palette"]],
      ];
      if (installed.includes(TRANSLATOR)) expected.push([TRANSLATOR, "text.translate", "翻译文本", "Translate text", ["palette", "selection"]]);
      const problems: string[] = [];
      for (const [pluginId, service, zh, en, surfaces] of expected) {
        const entry = services.find((item) => item.pluginId === pluginId && item.service === service);
        if (!entry) { problems.push(`${service} missing`); continue; }
        if (entry.titleLocalized?.zh !== zh || entry.titleLocalized?.en !== en) problems.push(`${service} titles ${JSON.stringify(entry.titleLocalized)}`);
        const actual = [...entry.surfaces].sort();
        if (JSON.stringify(actual) !== JSON.stringify(surfaces)) problems.push(`${service} surfaces ${actual.join("+")}, want ${surfaces.join("+")}`);
      }
      if (problems.length) throw new Error(problems.join("; "));
      const mine = services.filter((item) => installed.includes(item.pluginId));
      return `${mine.length} entries from ${installed.length} plugins; ${expected.length} checked for zh/en titles and default surfaces`;
    });

    // b. Base64 of UTF-8 text from the command palette.
    await checks.run("b", "text.base64-encode \"hello 世界\"", async () => {
      const input = "hello 世界";
      const want = base64Utf8(input);
      const got = outputText(succeeded(await runService({ service: "text.base64-encode", surface: "palette", input }, timeout), "text.base64-encode"));
      if (got !== want) throw new Error(`got ${JSON.stringify(got)}, want ${want}`);
      return `succeeded, output ${got}`;
    });

    // c. Hash with an explicit algorithm param.
    await checks.run("c", "text.hash sha256", async () => {
      const input = `MinCode services ${config.nonce}`;
      const want = await sha256Hex(input);
      const got = outputText(succeeded(await runService({ service: "text.hash", surface: "selection", input, params: { algorithm: "sha256" } }, timeout), "text.hash")).trim();
      if (got.toLowerCase() !== want) throw new Error(`got ${JSON.stringify(got)}, want ${want}`);
      return `selection surface, digest ${got}`;
    });

    // d. PNG → JPEG, saved through plugin_services_output_save.
    await checks.run("d", "image.convert png→jpeg + output_save", async () => {
      const snapshot = succeeded(await runService({ service: "image.convert", surface: "palette", paths: [fixture.pngPath], params: { format: "jpeg" } }, timeout), "image.convert");
      const files = snapshot.files ?? [];
      if (files.length !== 1) throw new Error(`${files.length} output files: ${JSON.stringify(files)}`);
      const [file] = files;
      if (!file.name.toLowerCase().match(/\.jpe?g$/) || file.size <= 0) throw new Error(`unexpected output ${JSON.stringify(file)}`);
      await invokeBackend("plugin_services_output_save", { callId: snapshot.callId, token: file.token, path: `${fixture.outputDir}/convert.jpg` });
      return `output ${file.name} (${file.mime}, ${file.size} B) saved as convert.jpg; harness checks the bytes`;
    });

    // e. Compress keeps EXIF by default and strips it on request.
    await checks.run("e", "image.compress stripMetadata", async () => {
      const save = async (strip: boolean, name: string) => {
        const snapshot = succeeded(await runService({ service: "image.compress", surface: "fileMenu", paths: [fixture.exifJpegPath], params: { stripMetadata: strip } }, timeout), "image.compress");
        const files = snapshot.files ?? [];
        if (files.length !== 1) throw new Error(`${files.length} output files`);
        await invokeBackend("plugin_services_output_save", { callId: snapshot.callId, token: files[0].token, path: `${fixture.outputDir}/${name}` });
        return files[0];
      };
      const stripped = await save(true, "compress-strip.jpg");
      const kept = await save(false, "compress-keep.jpg");
      return `stripMetadata=true → ${stripped.name} ${stripped.size} B, false → ${kept.size} B (fileMenu surface); harness checks the APP1 segment`;
    });

    // g. ai-translator: clear failure without configuration, then the harness mock as an OpenAI-compatible engine.
    if (!installed.includes(TRANSLATOR)) {
      checks.skip("g", "text.translate", fixture.packages.aiTranslator ? "ai-translator did not install" : "ai-translator was not packaged");
    } else {
      await checks.run("g1", "text.translate unconfigured fails clearly", async () => {
        const snapshot = await runService({ service: "text.translate", surface: "palette", input: "你好，世界", params: { to: "en" } }, timeout);
        if (snapshot.state !== "failed") throw new Error(`expected failed, got ${snapshot.state} ${JSON.stringify(snapshot.output)}`);
        if (!snapshot.error) throw new Error("failed without an error message");
        return `failed as expected: ${snapshot.error}`;
      });
      if (!fixture.ai) {
        checks.skip("g2", "text.translate via mock engine", "no mock AI fixture");
      } else {
        await checks.run("g2", "text.translate via the OpenAI-compatible mock", async () => {
          const ai = fixture.ai!;
          const session = await beginPluginUISession(TRANSLATOR, "main");
          try {
            await invokePluginUI(session.sessionId, session.nonce, "saveSettings", JSON.stringify({
              engine: "openai", openai: { baseUrl: ai.baseUrl, model: ai.model }, openaiKey: ai.apiKey,
            }));
          } finally {
            closePluginUISession(session.sessionId);
          }
          const snapshot = succeeded(await runService({ service: "text.translate", surface: "selection", input: ai.sourceText, params: { to: "en" } }, timeout), "text.translate");
          const got = outputText(snapshot);
          if (got !== ai.expectedText) throw new Error(`got ${JSON.stringify(got)}, want ${JSON.stringify(ai.expectedText)}`);
          return `settings saved through the page action (engine openai → loopback mock), output ${JSON.stringify(got)}`;
        });
      }
    }

    // h. Plugin-to-plugin call by service name from service-consumer-demo.
    if (!fixture.packages.consumerDemo) {
      checks.skip("h", "service-consumer-demo → text.base64-encode", "consumer demo was not built");
    } else {
      await checks.run("h", "service-consumer-demo → text.base64-encode (host authorization answered)", async () => {
        await install(fixture.packages.consumerDemo!, CONSUMER);
        installed.push(CONSUMER);
        const session = await beginPluginUISession(CONSUMER, "main");
        const approval = approveNextPluginCall(CONSUMER, timeout);
        try {
          const text = `consumer ${config.nonce} 你好`;
          const started = (await invokePluginUI(session.sessionId, session.nonce, "page.start", JSON.stringify({ service: "text.base64-encode", text }))) as { callId: string };
          const prompt = await approval.done;
          if (!prompt) throw new Error("no authorization prompt was shown");
          const deadline = performance.now() + timeout;
          let snapshot = (await invokePluginUI(session.sessionId, session.nonce, "page.status", JSON.stringify({ callId: started.callId }))) as Snapshot;
          while (snapshot.state === "running" && performance.now() < deadline) {
            await delay(100);
            snapshot = (await invokePluginUI(session.sessionId, session.nonce, "page.status", JSON.stringify({ callId: started.callId }))) as Snapshot;
          }
          succeeded(snapshot, "text.base64-encode from the consumer");
          const got = outputText(snapshot);
          if (got !== base64Utf8(text)) throw new Error(`got ${JSON.stringify(got)}, want ${base64Utf8(text)}`);
          const call = prompt.kind === "pluginCall" ? prompt.call : undefined;
          return `prompt ${prompt.kind} target=${call?.targetPluginId} risk=${call?.risk} answered "once"; output ${got}`;
        } finally {
          approval.stop();
          closePluginUISession(session.sessionId);
        }
      });
    }

    // i. image.ocr on the invoice scan (palette surface, file path input).
    if (!fixture.packages.ocr || !fixture.invoiceImagePath) {
      checks.skip("i", "image.ocr invoice.jpg", "ocr was not packaged");
    } else if (await checks.run("i1", "install ocr", async () => {
      await install(fixture.packages.ocr!, OCR);
      installed.push(OCR);
    })) {
      await checks.run("i", "image.ocr invoice.jpg (palette, paths)", async () => {
        const started = performance.now();
        const text = outputText(succeeded(await runService({ service: "image.ocr", surface: "palette", paths: [fixture.invoiceImagePath!] }, Math.max(timeout, 120_000)), "image.ocr"));
        const flat = text.replace(/\s+/g, "").replace(/￥/g, "¥");
        const missing = [INVOICE_NUMBER, `¥${INVOICE_TOTAL}`].filter((want) => !flat.includes(want));
        if (missing.length) throw new Error(`missing ${missing.join(", ")} in ${JSON.stringify(text.slice(0, 400))}`);
        return `${text.split("\n").length} lines in ${Math.round(performance.now() - started)} ms; contains 发票号码 ${INVOICE_NUMBER} and ¥${INVOICE_TOTAL}`;
      });
    }

    // j. invoice-manager imports the same scan through OCR (plugin-to-plugin, host authorization answered).
    if (!installed.includes(OCR) || !fixture.packages.invoiceManager) {
      checks.skip("j", "invoice-manager importFiles via OCR", fixture.packages.invoiceManager ? "ocr did not install" : "invoice-manager was not packaged");
    } else {
      await checks.run("j", "invoice-manager importFiles invoice.jpg → image.ocr (authorization answered)", async () => {
        await install(fixture.packages.invoiceManager!, INVOICES);
        installed.push(INVOICES);
        const session = await beginPluginUISession(INVOICES, "main");
        const approval = approveNextPluginCall(INVOICES, Math.max(timeout, 120_000));
        try {
          let task = await startPluginUITask(session.sessionId, session.nonce, "importFiles", JSON.stringify({ paths: [fixture.invoiceImagePath!] }), 180_000);
          const deadline = performance.now() + Math.max(timeout, 180_000);
          while (task.state === "running") {
            if (performance.now() > deadline) throw new Error("importFiles still running");
            await delay(200);
            task = await getPluginUITask(session.sessionId, session.nonce, task.taskId);
          }
          const prompt = await approval.done;
          if (task.state !== "succeeded") throw new Error(`importFiles ended ${task.state}: ${task.error ?? ""}`);
          if (!prompt) throw new Error("no authorization prompt was shown");
          const result = task.result as { results?: { status: string; id?: string; reason?: string; message?: string }[] };
          const row = result.results?.[0];
          if (result.results?.length !== 1 || row?.status !== "added" || !row.id) throw new Error(`results ${JSON.stringify(result.results)}`);
          const detail = (await invokePluginUI(session.sessionId, session.nonce, "get", JSON.stringify({ id: row.id }))) as {
            invoice: { number?: string; total: number; source: { method: string; format: string }; issues?: string[] }; review: boolean;
          };
          const { invoice } = detail;
          const problems: string[] = [];
          if (invoice.source.method !== "ocr") problems.push(`method ${invoice.source.method}`);
          if (!detail.review || !invoice.issues?.includes("ocr")) problems.push(`review ${detail.review} issues ${JSON.stringify(invoice.issues)}`);
          if (invoice.number !== INVOICE_NUMBER) problems.push(`number ${invoice.number}`);
          if (invoice.total !== 123456) problems.push(`total ${invoice.total}`);
          if (problems.length) throw new Error(problems.join("; "));
          const call = prompt.kind === "pluginCall" ? prompt.call : undefined;
          return `prompt target=${call?.targetPluginId} answered "once"; added ${row.id} (${invoice.source.format}), method ocr, needs review, number ${invoice.number}, total ¥${(invoice.total / 100).toFixed(2)}`;
        } finally {
          approval.stop();
          closePluginUISession(session.sessionId);
        }
      });
    }

    // f. A disabled provider's services are declared but inactive and leave the live list.
    await checks.run("f", "plugin_services_declared after disabling crypto-toolkit", async () => {
      await setPluginEnabled(CRYPTO, false);
      const declared = await declaredServices(CRYPTO);
      if (declared.length !== 8) throw new Error(`declared ${declared.length} services, want 8`);
      const active = declared.filter((item) => item.active !== false || item.inactiveReason !== "disabled");
      if (active.length) throw new Error(`not inactive/disabled: ${active.map((item) => `${item.service}(${item.active},${item.inactiveReason})`).join(", ")}`);
      const live = (await listServices()).filter((item) => item.pluginId === CRYPTO);
      if (live.length) throw new Error(`plugin_services_list still has ${live.length} crypto-toolkit entries`);
      const failed = await invokeBackend<Snapshot>("plugin_services_start", { service: "text.base64-encode", surface: "palette", input: "x" })
        .then((snapshot) => `started ${snapshot.state}`, (error) => nativeErrorMessage(error));
      if (failed.startsWith("started")) throw new Error(`disabled provider still ran: ${failed}`);
      return `8 declared, all active=false inactiveReason=disabled; list empty for it; start → ${failed}`;
    });
  } finally {
    for (const pluginId of installed.reverse()) await uninstallPlugin(pluginId, true).catch(() => undefined);
  }
  if (checks.failures.length) throw new Error(`${checks.failures.length} service check(s) failed: ${checks.failures.join(" | ")}`);
}
