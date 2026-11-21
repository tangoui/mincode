import { mount } from "@vue/test-utils";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { ElMessageBox } from "element-plus";
import { describe, expect, it, vi } from "vitest";
import { confirmPluginLaunch } from "@/services/plugin-launch-confirm";
import PluginLaunchConfirmContent from "@/components/plugins/PluginLaunchConfirmContent.vue";
import type { ResolvedProcessLaunch } from "@/services/plugin-launch-confirm";

const launch: ResolvedProcessLaunch = {
  pluginId: "acme.agents",
  pluginName: "Acme Agents",
  pluginVersion: "1.0.0",
  point: "acpAgents",
  entryKey: "helper",
  title: "Helper",
  argv: ["/plugins/acme.agents/bin/agent", "--acp"],
  env: {},
  cwd: "${projectRoot}",
  pluginExecutable: true,
  fingerprint: "d".repeat(64),
  trusted: false,
  previouslyTrusted: false,
};

describe("PluginLaunchConfirmContent", () => {
  it("bounds the confirmation to the viewport and scrolls only its content, keeping actions outside", async () => {
    // Removing the viewport cap or the shrinkable content region regresses long Java launches.
    // happy-dom checks the applied CSS contract; real viewport geometry is covered by browser smoke.
    const style = document.createElement("style");
    style.textContent = readFileSync(resolve(process.cwd(), "src/styles/shared/element-plus-controls.css"), "utf8");
    document.head.append(style);
    const result = confirmPluginLaunch({ ...launch, previouslyTrusted: true }, {
      notices: Array.from({ length: 80 }, (_, i) => `Classpath entry ${i}: /project/dependencies/library-${i}.jar`),
    });
    try {
      await vi.waitFor(() => expect(document.querySelector(".app-plugin-launch-confirm")).not.toBeNull());
      const dialog = document.querySelector<HTMLElement>(".app-plugin-launch-confirm")!;
      const content = dialog.querySelector<HTMLElement>(".el-message-box__content")!;
      const actions = dialog.querySelector<HTMLElement>(".el-message-box__btns")!;
      const dialogStyle = getComputedStyle(dialog);
      expect(dialogStyle.display).toBe("inline-flex");
      expect(dialogStyle.flexDirection).toBe("column");
      expect(dialogStyle.width).toBe("720px");
      expect(dialogStyle.maxWidth).toBe("100%");
      expect(dialogStyle.maxHeight).toBe("min(640px, 75dvh)");
      expect(Number.parseFloat(getComputedStyle(content).minHeight)).toBe(0);
      expect(getComputedStyle(content).overflow).toBe("hidden");
      expect(dialog.querySelectorAll(".el-scrollbar")).toHaveLength(1);
      expect(dialog.querySelector(".el-scrollbar__wrap")).not.toBeNull();
      expect(content.contains(actions)).toBe(false);
      expect(getComputedStyle(actions).flexShrink).toBe("0");
      expect(dialog.querySelectorAll("[data-launch-notice]")).toHaveLength(80);
      actions.querySelector<HTMLButtonElement>("button")!.click();
      expect(await result).toBe(false);
    } finally {
      ElMessageBox.close();
      style.remove();
    }
  });

  it("lists the caller's notices right below the command", () => {
    const wrapper = mount(PluginLaunchConfirmContent, {
      props: { launch, notices: ["Gets all environment variables", "Gets connectors: github"] },
      global: { stubs: { ElAlert: true } },
    });
    const items = wrapper.findAll("[data-launch-notice]");
    expect(items.map((item) => item.text())).toEqual(["Gets all environment variables", "Gets connectors: github"]);
    const html = wrapper.html();
    expect(html.indexOf("/plugins/acme.agents/bin/agent")).toBeLessThan(html.indexOf("Gets all environment variables"));
    expect(html.indexOf("Gets connectors: github")).toBeLessThan(html.indexOf("${projectRoot}"));
  });

  it("renders no notice block without notices", () => {
    const wrapper = mount(PluginLaunchConfirmContent, { props: { launch }, global: { stubs: { ElAlert: true } } });
    expect(wrapper.find("[data-launch-notice]").exists()).toBe(false);
  });
});
