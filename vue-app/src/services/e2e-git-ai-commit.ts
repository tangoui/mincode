// Loaded only by the native -tags e2e smoke runner. Drives the real Git panel's
// "AI generate" commit-message button against a loopback OpenAI-compatible
// endpoint (tools/e2e/desktop-smoke.mjs startHangingAi) and checks what the user
// sees. Every run starts with a single click after the staged status loaded, the
// first time the repository's draft is created: that is the path where the draft
// used to be a non-reactive object and the button kept spinning after the run.
//   1. a silent provider is stopped by the no-activity timeout, with a message,
//      and the button comes back;
//   2. a reasoning model that thinks for 45 s is not cut off, shows its progress
//      in the empty message box and fills the draft;
//   3. Cancel ends a run at once and it stays ended.
import router from "@/router";
import { enqueueProjectFiles } from "@/services/external-file-open";
import { readSettingsSnapshot, updateSettingsRuntimeSnapshot } from "@/services/settings-persistence";
import { useLocalProjectStore } from "@/stores/local-project";
import { useWorkbenchStore } from "@/stores/workbench";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

async function until<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = read();
    if (value) return value;
    await sleep(100);
  }
  throw new Error(`git-ai-commit smoke timed out: ${label}`);
}

/** The commit box's AI button (the plain el-button in the commit header). The box
 * may sit in a collapsed section; its state is what matters here. */
function aiButton(): HTMLButtonElement | undefined {
  return document.querySelector(".git-commit-box__header")?.querySelector<HTMLButtonElement>(":scope > .el-button") ?? undefined;
}

function messageBox(): HTMLTextAreaElement | undefined {
  return document.querySelector<HTMLTextAreaElement>(".git-commit-message-scroll textarea") ?? undefined;
}

/** Button label, "*" while it spins. */
function state() {
  const button = aiButton();
  const text = button?.textContent?.replace(/\s+/g, "") ?? "(no button)";
  return `${text}${button?.querySelector(".is-generating") ? "*" : ""}`;
}

const generating = () => state().endsWith("*");

function toasts(): string[] {
  // ElMessage is position: fixed (offsetParent is always null); any rendered message counts.
  return [...document.querySelectorAll<HTMLElement>(".el-message")].map((item) => item.textContent?.trim() ?? "");
}

/** One click, as a user would; the run must visibly start. */
async function start(timeoutMs: number) {
  aiButton()?.click();
  await until(() => generating(), `AI generation to start (button "${state()}", toasts ${JSON.stringify(toasts())})`, timeoutMs);
}

export async function gitAiCommitScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.gitAiCommit!;
  const project = useLocalProjectStore();
  const workbench = useWorkbenchStore();
  const previous = readSettingsSnapshot();
  updateSettingsRuntimeSnapshot({
    ...previous,
    aiProfiles: [{ id: "e2e-ai", name: "E2E", provider: "openai", apiFormat: "openai", baseUrl: fixture.baseUrl, apiKey: fixture.apiKey, model: fixture.model }],
    activeAiProfileId: "e2e-ai",
  });
  try {
    if (!workbench.openView("projects")) throw new Error("Could not open project workspace");
    await router.push({ name: "projects" });
    enqueueProjectFiles([fixture.repoRoot]);
    await until(() => project.projectRoot === fixture.repoRoot, "repository to open", config.timeoutMs);
    await router.push({ name: "projects", query: { panel: "git" } });
    await until(() => aiButton() && !aiButton()!.disabled, "AI generate button", config.timeoutMs);
    // The staged count in the commit box: clicking before it loads only warns.
    await until(() => /^1\s/.test(document.querySelector(".git-commit-submit-row > span")?.textContent?.trim() ?? ""), "staged status", config.timeoutMs);
    log.done(`git panel open with 1 staged file; button "${state()}"`);

    // 1. Silent provider: stopped by the 30 s no-activity timeout, with a message.
    await start(config.timeoutMs);
    let started = performance.now();
    await until(() => !generating(), `silent provider to be stopped (button "${state()}", toasts ${JSON.stringify(toasts())})`, 45_000);
    const silentSeconds = Math.round((performance.now() - started) / 1000);
    const silentToast = toasts().find((text) => text.includes(fixture.model)) ?? "";
    if (silentSeconds < 25) throw new Error(`silent provider stopped too early (${silentSeconds}s)`);
    if (!silentToast) throw new Error(`no timeout message after the silent provider stopped: ${JSON.stringify(toasts())}`);
    if (messageBox()?.value) throw new Error(`draft changed by a failed run: ${JSON.stringify(messageBox()?.value)}`);
    log.done(`silent provider stopped after ${silentSeconds}s, button back to "${state()}", message "${silentToast}"`);
    await sleep(3500); // the message closes, so step 2 only sees its own

    // 2. Reasoning model thinking for 45 s: not cut off, progress shown, draft filled.
    const idlePlaceholder = messageBox()?.placeholder ?? "";
    await start(config.timeoutMs);
    started = performance.now();
    const thinking = await until(() => {
      const placeholder = messageBox()?.placeholder ?? "";
      return placeholder !== idlePlaceholder && /\d/.test(placeholder) ? placeholder : undefined;
    }, "thinking progress in the message box", 15_000);
    await sleep(3000);
    const later = messageBox()?.placeholder ?? "";
    if (later === thinking) throw new Error(`thinking progress did not advance: ${JSON.stringify(thinking)}`);
    log.done(`while thinking the empty message box says "${thinking}", then "${later}"`);
    await until(() => !generating(), `reasoning model to finish (button "${state()}")`, 90_000);
    const thinkSeconds = Math.round((performance.now() - started) / 1000);
    const value = messageBox()?.value ?? "";
    if (value !== fixture.answer) throw new Error(`reasoning model result not in the draft after ${thinkSeconds}s: ${JSON.stringify(value)} toasts=${JSON.stringify(toasts())}`);
    if (thinkSeconds < 40) throw new Error(`reasoning model finished suspiciously early (${thinkSeconds}s)`);
    if ((messageBox()?.placeholder ?? "") !== idlePlaceholder) throw new Error("thinking text stayed after the answer");
    log.done(`reasoning model answered after ${thinkSeconds}s; draft is "${value}"`);

    // 3. Cancel ends a run at once and it stays ended.
    await start(config.timeoutMs);
    await sleep(2000);
    aiButton()?.click();
    const after: string[] = [];
    for (let i = 0; i < 8; i++) {
      await sleep(500);
      after.push(state());
    }
    if (after.some((label) => label.endsWith("*"))) throw new Error(`"Cancel" came back after cancelling: ${after.join(", ")}`);
    if ((messageBox()?.value ?? "") !== fixture.answer) throw new Error(`a cancelled run changed the draft: ${JSON.stringify(messageBox()?.value)}`);
    log.done(`cancel stopped the run and it stayed stopped; the draft kept "${fixture.answer}"`);
  } finally {
    updateSettingsRuntimeSnapshot(previous);
  }
}
