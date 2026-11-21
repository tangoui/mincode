/**
 * Shared host side of a plugin menu (contributes.menus): turns "the target of
 * the menu that is open right now" into the `host` and `context` props of
 * components/plugins/PluginMenuItems.vue.
 *
 * `target` returns the single object the user opened the menu on, or null
 * while the menu is closed (then `host` is null and no `when` is evaluated).
 * For per-row "⋯" dropdowns, record the open row with `@visible-change` and
 * return a target only for that row. `context` builds the whitelisted
 * passContext object lazily, at click time; without a target it throws
 * MenuTargetUnavailableError (reported to the user by runPluginMenuItem)
 * instead of sending an empty context.
 *
 * This and domain/plugins/service-surfaces.ts (services on the same
 * targets) are the only importers of domain/plugins/menu-targets.ts, which
 * keeps the placement builders out of the entry chunk.
 */
import { computed, type ComputedRef } from "vue";
import type { PluginContextValues } from "@/domain/commands/when";
import { MenuTargetUnavailableError, type PluginContextMenuId } from "@/domain/plugins/menus";
import { menuHostContext, menuTargetPassContext, type PluginMenuTargetOf } from "@/domain/plugins/menu-targets";

export function usePluginMenuTarget<M extends PluginContextMenuId>(
  menu: M,
  target: () => PluginMenuTargetOf<M> | null,
): { host: ComputedRef<PluginContextValues | null>; context: () => Record<string, unknown> } {
  const host = computed(() => {
    const current = target();
    return current ? menuHostContext(menu, current) : null;
  });
  const context = () => {
    const current = target();
    if (!current) throw new MenuTargetUnavailableError(menu);
    return menuTargetPassContext(menu, current);
  };
  return { host, context };
}
