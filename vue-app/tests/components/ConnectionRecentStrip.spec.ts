import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import ConnectionAvatar from "@/components/connections/ConnectionAvatar.vue";
import ConnectionRecentStrip, { type RecentStripItem } from "@/components/connections/ConnectionRecentStrip.vue";
import { avatarColor, avatarInitials } from "@/domain/connections/overview";

const items: RecentStripItem[] = [
  { id: "a", name: "geo线上", seed: "a", detail: "CLAW · 10 分钟前", status: "pending" },
  { id: "b", name: "veve", seed: "b", detail: "支持部 · 正在使用", status: "online" },
  { id: "c", name: "openclaw", seed: "c", detail: "支持部 · 3 天前", status: "offline" },
];

function mountStrip(list: RecentStripItem[] = items) {
  return mount(ConnectionRecentStrip, {
    props: { items: list, title: "最近打开", clearLabel: "清除记录" },
  });
}

describe("ConnectionRecentStrip", () => {
  it("renders one keyboard-focusable card per item with name and detail", () => {
    const wrapper = mountStrip();
    const cards = wrapper.findAll(".connection-overview__recent-card");

    expect(wrapper.text()).toContain("最近打开");
    expect(cards).toHaveLength(3);
    items.forEach((item, index) => {
      const card = cards[index];
      expect(card.element.tagName).toBe("BUTTON");
      expect(card.attributes("type")).toBe("button");
      expect(card.attributes("aria-label")).toBe(item.name);
      expect(card.text()).toContain(item.name);
      expect(card.text()).toContain(item.detail);
      // The name is the accessible name; detail (time / "in use") is announced as its description.
      const detailId = card.attributes("aria-describedby");
      expect(detailId).toBeTruthy();
      expect(card.get(`[id="${detailId}"]`).text()).toBe(item.detail);
    });
    expect(cards[1].find(".connection-avatar__status").classes()).toContain("is-online");
  });

  it("emits open with the clicked card id and clear from the clear action", async () => {
    const wrapper = mountStrip();

    await wrapper.findAll(".connection-overview__recent-card")[1].trigger("click");
    expect(wrapper.emitted("open")).toEqual([["b"]]);

    const clear = wrapper.get(".connection-overview__section-action");
    expect(clear.text()).toBe("清除记录");
    await clear.trigger("click");
    expect(wrapper.emitted("clear")).toEqual([[]]);
  });

  it("renders nothing when there are no items", () => {
    const wrapper = mountStrip([]);
    expect(wrapper.html()).toBe("<!--v-if-->");
  });

  it("lets callers replace the avatar through the avatar slot", () => {
    const wrapper = mount(ConnectionRecentStrip, {
      props: { items, title: "最近打开", clearLabel: "清除记录" },
      slots: { avatar: `<template #avatar="{ item }"><i class="custom-avatar">{{ item.id }}</i></template>` },
    });
    expect(wrapper.findAll(".custom-avatar").map((node) => node.text())).toEqual(["a", "b", "c"]);
    expect(wrapper.find(".connection-avatar").exists()).toBe(false);
  });
});

describe("ConnectionAvatar", () => {
  it("shows initials on the seed color with a status dot", () => {
    const wrapper = mount(ConnectionAvatar, { props: { name: "veve prod", seed: "seed-1", status: "online" } });
    const root = wrapper.get(".connection-avatar");

    expect(root.text()).toContain(avatarInitials("veve prod"));
    expect(root.attributes("style")).toContain(`--connection-avatar-color: ${avatarColor("seed-1")}`);
    expect(root.classes()).toContain("is-size-32");
    expect(wrapper.get(".connection-avatar__status").classes()).toContain("is-online");
  });

  it("omits the status dot when no status is given and accepts slot content", () => {
    const wrapper = mount(ConnectionAvatar, {
      props: { name: "db", seed: "s", size: 26 },
      slots: { default: `<b class="icon">M</b>` },
    });
    expect(wrapper.find(".connection-avatar__status").exists()).toBe(false);
    expect(wrapper.get(".connection-avatar").classes()).toContain("is-size-26");
    expect(wrapper.find(".icon").exists()).toBe(true);
    expect(wrapper.text()).not.toContain(avatarInitials("db"));
  });
});
