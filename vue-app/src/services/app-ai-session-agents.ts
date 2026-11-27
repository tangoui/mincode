import { ACP_BUILTIN_AGENTS } from "@/domain/ai/acp-agents";
import { hydrateSettingsSnapshot, readSettingsSnapshot } from "@/services/settings-persistence";
import { redactSensitiveText } from "./ai-tools/shared";

export async function listAiSessionAgents() {
  await hydrateSettingsSnapshot();
  const { acpAgents } = readSettingsSnapshot<{ acpAgents?: Array<{ id: string; name: string; command: string }> }>();
  return [
    ...ACP_BUILTIN_AGENTS.map(({ id, name }) => ({ id, name })),
    ...(acpAgents ?? []).filter(({ id, name, command }) => id && name?.trim() && command?.trim())
      .map(({ id, name }) => ({ id: `custom:${id}`, name: redactSensitiveText(name) })),
  ];
}
