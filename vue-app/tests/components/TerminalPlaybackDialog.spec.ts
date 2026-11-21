import { afterEach, describe, expect, it, vi } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { nextTick } from "vue";

// The dialog owns an xterm instance; the list is what these cases assert, so the
// terminal and the host services it talks to are replaced with no-ops.
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    open() {}
    loadAddon() {}
    reset() {}
    write() {}
    dispose() {}
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@/services/terminal-preferences", () => ({
  readTerminalPreferences: () => ({ scrollback: 1000 }),
  attachTerminalWebglAddon: () => null,
}));
vi.mock("@/services/terminal-theme", () => ({
  resolveTerminalTheme: () => ({}),
  setTerminalTheme: () => {},
  TERMINAL_COLOR_SCHEME_CHANGED_EVENT: "mincode:terminal-color-scheme",
}));
vi.mock("@/services/ui-theme", () => ({ UI_THEME_CHANGED_EVENT: "mincode:ui-theme" }));

import TerminalPlaybackDialog from "@/components/terminal/TerminalPlaybackDialog.vue";
import { createTerminalRecording, type SavedTerminalRecording } from "@/services/terminal-tools";

const stubs = {
  ElDialog: { props: ["modelValue"], template: "<div><slot /></div>" },
  ElTooltip: { template: "<div><slot /></div>" },
  ElSelect: { template: "<div><slot /></div>" },
  ElOption: { template: "<div />" },
};

const SAVED_AT = new Date("2026-11-04T10:13:56").getTime();

function recording(overrides: Partial<SavedTerminalRecording> = {}): SavedTerminalRecording {
  return {
    id: "r1",
    name: new Date(SAVED_AT).toLocaleString(),
    frames: [
      { t: 0, d: "a" },
      { t: 100, d: "b" },
    ],
    createdAt: SAVED_AT,
    ...overrides,
  };
}

let wrapper: VueWrapper | undefined;

function mountDialog(recordings: readonly SavedTerminalRecording[]) {
  wrapper = mount(TerminalPlaybackDialog, {
    props: { modelValue: true, recordings },
    global: { stubs },
  });
  return wrapper;
}

function rows(view: VueWrapper) {
  return view.findAll('[role="option"]');
}

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

describe("terminal playback recordings list", () => {
  it("shows the terminal the recording came from, then the time and frame count", async () => {
    const view = mountDialog([recording({ name: "geo线上" })]);
    await nextTick();

    const option = rows(view)[0];
    expect(option.exists()).toBe(true);
    expect(option.text()).toContain("geo线上");
    expect(option.find("small").text()).toContain("·");
    expect(option.text()).toContain("2");
    expect(option.text()).toContain("帧");
  });

  it("keeps the timestamp as a single line for recordings named after it", async () => {
    const view = mountDialog([recording()]);
    await nextTick();

    const option = rows(view)[0];
    expect(option.text()).toContain(new Date(SAVED_AT).toLocaleString());
    expect(option.find("small").text()).not.toContain("·");
  });

  it("keeps the rename and delete actions inside the row they belong to", async () => {
    const view = mountDialog([recording({ name: "geo线上" })]);
    await nextTick();

    const option = rows(view)[0];
    const actions = option.findAll("button");
    expect(actions).toHaveLength(2);

    await actions[1].trigger("click");
    expect(view.emitted("delete")?.[0]).toEqual(["r1"]);
  });
});

describe("searching recordings", () => {
  it("filters by name and keeps the player on a visible recording", async () => {
    const view = mountDialog([
      recording({ id: "r1", name: "geo线上" }),
      recording({ id: "r2", name: "build 日志" }),
    ]);
    await nextTick();

    const search = view.find(".terminal-playback-dialog__search input");
    await search.setValue("build");
    await nextTick();

    const visible = rows(view);
    expect(visible).toHaveLength(1);
    expect(visible[0].text()).toContain("build 日志");
    expect(visible[0].attributes("aria-selected")).toBe("true");
  });

  it("shows the empty state when nothing matches", async () => {
    const view = mountDialog([recording({ name: "geo线上" })]);
    await nextTick();

    await view.find(".terminal-playback-dialog__search input").setValue("没有这个");
    await nextTick();

    expect(rows(view)).toHaveLength(0);
  });
});

describe("renaming recordings", () => {
  it("opens an inline editor from the row action and emits the new name on Enter", async () => {
    const view = mountDialog([recording({ id: "r1", name: "geo线上" })]);
    await nextTick();

    await rows(view)[0].findAll("button")[0].trigger("click");
    await nextTick();

    const input = rows(view)[0].find("input");
    expect(input.exists()).toBe(true);
    await input.setValue("生产环境");
    await input.trigger("keydown.enter");

    expect(view.emitted("rename")?.[0]).toEqual(["r1", "生产环境"]);
  });

  it("starts editing on double click and cancels with Escape", async () => {
    const view = mountDialog([recording({ id: "r1", name: "geo线上" })]);
    await nextTick();

    await rows(view)[0].trigger("dblclick");
    await nextTick();

    const input = rows(view)[0].find("input");
    expect(input.exists()).toBe(true);
    await input.setValue("不要保存");
    await input.trigger("keydown.esc");

    expect(view.emitted("rename")).toBeUndefined();
  });
});

describe("createTerminalRecording naming", () => {
  it("uses the terminal name and only falls back to the timestamp without one", () => {
    expect(createTerminalRecording([], "geo线上", SAVED_AT).name).toBe("geo线上");
    expect(createTerminalRecording([], "   ", SAVED_AT).name).toBe(new Date(SAVED_AT).toLocaleString());
    expect(createTerminalRecording([], undefined, SAVED_AT).name).toBe(new Date(SAVED_AT).toLocaleString());
  });
});
