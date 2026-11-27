import { registerNativeAiCapabilities, type NativeAiCapabilityDependencies } from "./ai-native-tools";
import { aiSessionCapabilities, type AiSessionToolDependencies } from "./ai-tools/ai-session-tools";
import { createNotesCapabilities } from "./ai-tools/notes-tools";
import { createUiCapabilities, type UiNavigationDependencies } from "./ai-tools/ui-tools";
import { createDashboardCapabilities, type DashboardToolDependencies } from "./ai-tools/dashboard-tools";

export type AiCapabilityHostDeps = Omit<
  NativeAiCapabilityDependencies,
  "askUser" | "runSubagentPlan"
> & { aiSessions?: AiSessionToolDependencies; uiNavigation?: UiNavigationDependencies; dashboard?: DashboardToolDependencies };

export function startAiCapabilityHost(deps: AiCapabilityHostDeps): () => void {
  const stopNative = registerNativeAiCapabilities(deps);
  const registrations = [
    ...(deps.aiSessions ? aiSessionCapabilities(deps.aiSessions) : []),
    ...(deps.nativeRuntime ? createNotesCapabilities() : []),
    ...(deps.uiNavigation ? createUiCapabilities(deps.uiNavigation) : []),
    ...(deps.dashboard ? createDashboardCapabilities(deps.dashboard) : []),
  ].map((capability) => deps.capabilities.register(capability));
  return () => {
    registrations.forEach((stop) => stop());
    stopNative();
  };
}
