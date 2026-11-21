import { ElDropdown } from "element-plus";

type PropWithDefault = { default?: unknown };

// ElDropdown defaults to `persistent: true`, which mounts every menu (and all of
// its items/icons) up front and keeps it alive inside KeepAlive'd workspaces.
// Rendering menus only while open cuts thousands of idle component instances.
export function applyElementPlusDefaults() {
  const props = (ElDropdown as { props?: Record<string, PropWithDefault> }).props;
  const persistent = props?.persistent;
  if (persistent && typeof persistent === "object") persistent.default = false;
}
