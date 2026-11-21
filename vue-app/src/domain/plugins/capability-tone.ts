// Plugin capability tags on the plugin cards: each capability keeps one tone
// everywhere so users learn to recognize it. Known capabilities use a fixed
// palette spread so a card's common tags differ; unknown names hash into it.
export const PLUGIN_CAPABILITY_TONES = ["blue", "teal", "green", "olive", "amber", "orange", "red", "violet"] as const;
export type PluginCapabilityTone = (typeof PLUGIN_CAPABILITY_TONES)[number];

const KNOWN_TONES: Record<string, PluginCapabilityTone> = {
  commands: "blue",
  ui: "teal",
  storage: "green",
  secrets: "violet",
  network: "amber",
  notifications: "olive",
  shell: "red",
  "workspace.read": "orange",
  "workspace.write": "red",
  clipboard: "olive",
  ai: "violet",
  "ai.activity": "violet",
  "ai.tasks": "violet",
  plugins: "blue",
  "system.associations": "teal",
  "database.schema": "green",
  "database.driver": "orange",
  "ai.config": "orange",
  // Discloses remote endpoints and object names chosen in a menu.
  "remote.context": "amber",
  // Lets the plugin's pages run WebAssembly: a warning, not a data grant.
  "ui.wasm": "amber",
  // Reads and overwrites all of MinCode's data: the danger tone.
  backup: "red",
};

export function pluginCapabilityTone(capability: string): PluginCapabilityTone {
  const known = KNOWN_TONES[capability];
  if (known) return known;
  let hash = 0;
  for (const char of capability) hash = (Math.imul(hash, 31) + (char.codePointAt(0) ?? 0)) >>> 0;
  return PLUGIN_CAPABILITY_TONES[hash % PLUGIN_CAPABILITY_TONES.length]!;
}
