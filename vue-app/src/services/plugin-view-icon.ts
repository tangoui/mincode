import { defineAsyncComponent, type Component } from "vue";
import {
  Box, Connection, DataAnalysis, Document, Folder,
  Monitor, Promotion, Setting, Tickets,
} from "@element-plus/icons-vue";
import { PLUGIN_ICON_ALIASES, PLUGIN_ICONS } from "@/domain/plugin-icons";

const icons = new Map<string, Component>([
  ["box", Box],
  ["connection", Connection],
  ["data-analysis", DataAnalysis],
  ["document", Document],
  ["folder", Folder],
  ["monitor", Monitor],
  ["promotion", Promotion],
  ["setting", Setting],
  ["terminal", Monitor],
  ["tickets", Tickets],
]);
const allowed = new Set(PLUGIN_ICONS);

function pascal(name: string): string {
  return name.replace(/(^|-)([a-z])/g, (_match, _dash: string, letter: string) => letter.toUpperCase());
}

// Common icons stay in the main bundle; the rest of the Element Plus set loads on first use.
// The self-contained "global" build is imported so that the full icon set lands in its own
// lazy chunk instead of turning the shared barrel used by static imports into a preloaded chunk.
export function pluginViewIcon(icon?: string): Component {
  const name = icon?.trim() ?? "";
  const known = icons.get(name);
  if (known || !allowed.has(name)) return known ?? Box;
  const target = pascal(Object.prototype.hasOwnProperty.call(PLUGIN_ICON_ALIASES, name) ? PLUGIN_ICON_ALIASES[name] : name);
  const lazy = defineAsyncComponent({
    loader: () => import("@element-plus/icons-vue/global").then((module) =>
      (module as unknown as Record<string, Component | undefined>)[target] ?? Box),
    errorComponent: Box,
  });
  icons.set(name, lazy);
  return lazy;
}
