import { describe, expect, it } from "vitest";
import { nextTick } from "vue";
import { confirmPluginRegistryInstall } from "@/services/settings-plugin-catalog-review";
import type { RegistryEntry } from "@/domain/ai/settings-integrations";
const entry: RegistryEntry = {
  id: "demo", name: "Untrusted item", description: "", source: "plugin",
  plugin: { pluginId: "demo", pluginName: "<img src=x onerror=alert(1)>", catalogId: "team", itemId: "demo" },
};
const translate = (key: string, params?: Record<string, string | number>) => params ? `${key} ${params.name} ${params.plugin}` : key;
async function settle() { for (let i = 0; i < 8; i++) await nextTick(); }
describe("third-party catalog confirmation", () => {
  it("shows attribution and complete command as inert text, and cancellation refuses the add", async () => {
    const result = confirmPluginRegistryInstall(entry, 'node server.js\nenv TEAM=core\n<script>bad()</script>', translate);
    await settle();
    const box = document.querySelector('.el-message-box')!;
    expect(box.textContent).toContain('<img src=x onerror=alert(1)>');
    expect(box.querySelector('pre')?.textContent).toBe('node server.js\nenv TEAM=core\n<script>bad()</script>');
    expect(box.querySelector('img, script')).toBeNull();
    (box.querySelector('.el-message-box__btns button') as HTMLButtonElement).click();
    expect(await result).toBe(false);
  });
  it("accepts only after the user presses Add", async () => {
    const result = confirmPluginRegistryInstall(entry, 'https://example.com/mcp', translate);
    await settle();
    const boxes = document.querySelectorAll('.el-message-box');
    const box = boxes[boxes.length - 1];
    (box.querySelector('.el-button--primary') as HTMLButtonElement).click();
    expect(await result).toBe(true);
  });
});
