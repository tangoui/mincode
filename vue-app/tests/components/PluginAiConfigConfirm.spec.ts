import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import PluginAiConfigConfirm from "@/components/plugins/PluginAiConfigConfirm.vue";
import type { AiConfigReviewPrompt, AiConfigReviewRow } from "@/domain/plugin-ui/ai-config";
import { messagesZh } from "@/i18n/locales/zh";

const zh: Readonly<Record<string, string>> = messagesZh;
const translate = (key: string) => zh[key] ?? `missing:${key}`;

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => { wrapper?.unmount(); wrapper = undefined; });

const KEY = "mincode.ai-market/mcp/figma";

function mcpRow(fields: Partial<Extract<AiConfigReviewRow, { kind: "mcp" }>> = {}): AiConfigReviewRow {
  return {
    kind: "mcp",
    key: KEY,
    action: "add",
    name: "Figma",
    targetName: "Figma",
    sourceApp: "ai-market",
    duplicateName: false,
    conflict: false,
    secretsOnly: false,
    changedFields: [],
    transport: "stdio",
    commandLine: "npx -y figma-mcp@1.0.0",
    url: "",
    envKeys: ["FIGMA_API_KEY"],
    headerKeys: [],
    secretValues: "none",
    auth: "none",
    prompts: [],
    ...fields,
  };
}

function prompt(fields: Partial<AiConfigReviewPrompt> = {}): AiConfigReviewPrompt {
  return { target: "env", name: "FIGMA_API_KEY", label: "Figma API key", secret: true, filled: false, ...fields };
}

async function open(rows: AiConfigReviewRow[]) {
  wrapper = mount(PluginAiConfigConfirm, { props: { pluginName: "AI Market", rows }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

const writeButton = () =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find(
    (item) => item.textContent?.trim() === translate("plugins.aiConfig.write"),
  );
const inputs = () => [...document.querySelectorAll<HTMLInputElement>(".app-plugin-ai-config__prompts input")];

describe("plugin ai.config confirmation", () => {
  it("asks for unfilled prompts and blocks confirm until filled", async () => {
    const dialog = await open([mcpRow({ prompts: [prompt()] })]);
    const [input] = inputs();
    expect(inputs()).toHaveLength(1);
    expect(input.type).toBe("password");
    expect(document.body.textContent).toContain("Figma API key");
    expect(writeButton()?.disabled).toBe(true);

    writeButton()?.click();
    await flushPromises();
    expect(dialog.emitted("confirm")).toBeUndefined();

    input.value = "  ";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(writeButton()?.disabled, "blank is not a value").toBe(true);

    input.value = "abc";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(writeButton()?.disabled).toBe(false);
    writeButton()?.click();
    await flushPromises();
    expect(dialog.emitted("confirm")).toEqual([[{ [KEY]: { "env:FIGMA_API_KEY": "abc" } }]]);
    expect(inputs().every((item) => item.value === ""), "inputs are cleared once the dialog closes").toBe(true);
  });

  it("labels a prompt by its name when it has no label and shows plain values as text", async () => {
    await open([mcpRow({ prompts: [prompt({ target: "header", name: "X-Team", label: "", secret: false })] })]);
    const [input] = inputs();
    expect(input.type).toBe("text");
    expect(input.getAttribute("aria-label")).toContain("X-Team");
  });

  it("filled prompts are not asked again", async () => {
    const dialog = await open([
      mcpRow({
        action: "overwrite",
        prompts: [prompt({ filled: true }), prompt({ target: "header", name: "Authorization", label: "Token", filled: false })],
      }),
    ]);
    expect(inputs()).toHaveLength(1);
    expect(document.body.textContent).not.toContain("Figma API key");
    inputs()[0].value = "pat";
    inputs()[0].dispatchEvent(new Event("input"));
    await flushPromises();
    writeButton()?.click();
    await flushPromises();
    expect(dialog.emitted("confirm")).toEqual([[{ [KEY]: { "header:Authorization": "pat" } }]]);
  });

  it("confirms with no values when nothing is asked", async () => {
    const dialog = await open([mcpRow({ prompts: [prompt({ filled: true })] })]);
    expect(inputs()).toHaveLength(0);
    expect(writeButton()?.disabled).toBe(false);
    writeButton()?.click();
    await flushPromises();
    expect(dialog.emitted("confirm")).toEqual([[{}]]);
  });

  it("oauth rows say sign-in is needed after install", async () => {
    await open([mcpRow({ auth: "oauth", transport: "http", commandLine: "", url: "https://mcp.example.com/mcp" })]);
    expect(document.body.textContent).toContain(translate("plugins.aiConfig.oauthAfterInstall"));
  });

  it("rows without sign-in do not mention it", async () => {
    await open([mcpRow()]);
    expect(document.body.textContent).not.toContain(translate("plugins.aiConfig.oauthAfterInstall"));
  });
});
