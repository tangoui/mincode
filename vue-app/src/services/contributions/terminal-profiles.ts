/**
 * Consumer of the `terminalProfiles` contribution point (process plugins with
 * the `shell` capability). It stores the re-validated profiles for the
 * terminal "+" menu and registers one palette command per profile. Registering
 * data never starts anything: every launch goes through the host by reference
 * and needs the user's confirmation of the exact command line
 * (services/terminal-profile-service.ts).
 */
import { TERMINAL_PROFILE_COMMAND_PREFIX, TERMINAL_PROFILES_CONTRIBUTION_POINT, terminalProfileTitle } from "@/domain/terminal/profiles";
import { useVueI18n } from "@/i18n/messages";
import { registerCommand } from "@/services/command-registry";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { requestTerminalProfileTab } from "@/services/terminal-profile-service";
import { useTerminalProfilesStore } from "@/stores/terminal-profiles";

export function registerTerminalProfileContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: TERMINAL_PROFILES_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = useTerminalProfilesStore();
      const { t, localeChain } = useVueI18n();
      store.setContributions(contributions);
      const disposers = store.profiles.map((profile) =>
        registerCommand({
          id: `${TERMINAL_PROFILE_COMMAND_PREFIX}${profile.key}`,
          source: "workspace",
          title: () =>
            t("pluginContrib.terminalProfiles.paletteTitle", {
              title: terminalProfileTitle(profile, localeChain.value),
              plugin: profile.pluginName,
            }),
          keywords: ["terminal", "profile", profile.pluginId, profile.id],
          execute: () => requestTerminalProfileTab(profile.key),
        }),
      );
      return () => {
        for (const dispose of disposers) dispose();
        store.clearContributions();
      };
    },
  });
}
