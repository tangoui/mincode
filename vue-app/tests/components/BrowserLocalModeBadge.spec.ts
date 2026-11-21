import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { applySavedLocale } from "@/i18n/messages";

const bridge = vi.hoisted(() => ({ browser: true }));

vi.mock("@/services/desktop-bridge", () => ({
  isBrowserLocalMode: () => bridge.browser,
}));

import BrowserLocalModeBadge from "@/components/layout/BrowserLocalModeBadge.vue";

afterEach(() => {
  bridge.browser = true;
  document.body.innerHTML = "";
});

describe("BrowserLocalModeBadge", () => {
  it("renders nothing in the desktop runtime", () => {
    bridge.browser = false;
    const wrapper = mount(BrowserLocalModeBadge, { attachTo: document.body });
    expect(wrapper.find(".app-browser-local-badge").exists()).toBe(false);
    wrapper.unmount();
  });

  it("shows the labelled badge and opens the features and limits popover in browser mode", async () => {
    await applySavedLocale("en");
    const wrapper = mount(BrowserLocalModeBadge, { attachTo: document.body });
    const button = wrapper.get(".app-browser-local-badge");
    expect(button.text()).toBe("Browser local mode");
    expect(button.attributes("aria-label")).toBe("Browser local mode: view available features and limits");

    await button.trigger("click");
    await flushPromises();
    const popover = document.body.querySelector(".app-browser-local");
    expect(popover?.textContent).toContain("Available in the browser");
    expect(popover?.textContent).toContain("no features are available in the browser yet");
    expect(popover?.textContent).toContain("Use the desktop app for full functionality.");
    wrapper.unmount();
    await applySavedLocale("zh");
  });
});
