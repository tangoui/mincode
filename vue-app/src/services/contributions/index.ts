/**
 * Built-in frontend consumers of plugin contribution points. Adding a point:
 * write services/contributions/<point>.ts exporting a register function that
 * calls `registerContributionConsumer`, and add it to `consumers` below
 * (see docs/architecture/plugin-system.md, "Contribution points").
 */
import { registerAcpAgentContributionConsumer } from "@/services/contributions/acp-agents";
import { registerAiCatalogContributionConsumer } from "@/services/contributions/ai-catalogs";
import { registerAiProviderContributionConsumer } from "@/services/contributions/ai-providers";
import { registerAiContextProviderContributionConsumer } from "@/services/contributions/ai-context-providers";
import { registerAiPromptContributionConsumer } from "@/services/contributions/ai-prompts";
import { registerCommandSnippetContributionConsumer } from "@/services/contributions/command-snippets";
import { registerDataProviderContributionConsumers } from "@/services/contributions/data-providers";
import { registerCustomEditorContributionConsumer } from "@/services/contributions/custom-editors";
import { registerDatabaseDriverContributionConsumer } from "@/services/contributions/database-drivers";
import { registerFileIconContributionConsumer } from "@/services/contributions/file-icons";
import { registerFormatterContributionConsumer } from "@/services/contributions/formatters";
import { registerJsonValidationContributionConsumer } from "@/services/contributions/json-validation";
import { registerFileTemplateContributionConsumer } from "@/services/contributions/file-templates";
import { registerHttpCodeGeneratorContributionConsumer } from "@/services/contributions/http-code-generators";
import { registerHttpCollectionContributionConsumer } from "@/services/contributions/http-collections";
import { registerKeymapContributionConsumer } from "@/services/contributions/keymaps";
import { registerLanguageContributionConsumer } from "@/services/contributions/languages";
import { registerLanguageServerContributionConsumer } from "@/services/contributions/language-servers";
import { registerMcpServerContributionConsumer } from "@/services/contributions/mcp-servers";
import { registerTreeViewContributionConsumer } from "@/services/contributions/tree-views";
import { registerTestProviderContributionConsumer } from "@/services/contributions/test-providers";
import { registerLanguageFeatureContributionConsumer } from "@/services/contributions/language-features";
import { registerLinterContributionConsumer } from "@/services/contributions/linters";
import { registerLocalizationContributionConsumer } from "@/services/contributions/localizations";
import { registerMenuContributionConsumer } from "@/services/contributions/menus";
import { registerSnippetContributionConsumer } from "@/services/contributions/snippets";
import { registerSearchProviderContributionConsumer } from "@/services/contributions/search-providers";
import { registerStatusBarContributionConsumer } from "@/services/contributions/status-bar";
import { registerThemeContributionConsumer } from "@/services/contributions/themes";
import { registerEditorThemeContributionConsumer } from "@/services/contributions/editor-themes";
import { registerEditorFrameworkContributionConsumer } from "@/services/contributions/editor-frameworks";
import { registerTerminalProfileContributionConsumer } from "@/services/contributions/terminal-profiles";
import { registerTerminalColorSchemeContributionConsumer } from "@/services/contributions/terminal-color-schemes";
import { registerTaskContributionConsumer } from "@/services/contributions/tasks";
import { registerTerminalLinkContributionConsumer } from "@/services/contributions/terminal-links";
import { registerUriHandlerContributionConsumer } from "@/services/contributions/uri-handlers";
import { registerWalkthroughContributionConsumer } from "@/services/contributions/walkthroughs";
import { registerDashboardWidgetContributionConsumer } from "@/services/contributions/dashboard-widgets";
import { registerScheduleContributionConsumer } from "@/services/contributions/schedules";
import { registerSettingsPageContributionConsumer } from "@/services/contributions/settings-pages";
import { registerExternalOpenHandlerConsumer } from "@/services/contributions/external-open-handlers";
import { startContributionSync } from "@/services/plugin-contributions";

// Order is significant: consumers apply (and their Monaco hooks run) in this
// order. Language registrations must come before snippets so snippet
// languages contributed by plugins exist when snippet providers register.
const consumers: ReadonlyArray<() => () => void> = [
  // Language packs first so contributed messages exist before other
  // consumers resolve titles.
  registerLocalizationContributionConsumer,
  registerKeymapContributionConsumer,
  registerMenuContributionConsumer,
  registerLanguageContributionConsumer,
  registerEditorFrameworkContributionConsumer,
  registerSnippetContributionConsumer,
  // jsonValidation (Phase F; Monaco-backed, after languages/snippets)
  registerJsonValidationContributionConsumer,
  // formatters (Phase F; Monaco-backed, after languages/snippets)
  registerFormatterContributionConsumer,
  // linters (Phase G; Monaco markers, after formatters)
  registerLinterContributionConsumer,
  registerStatusBarContributionConsumer,
  registerThemeContributionConsumer,
  // editorThemes (after themes: an editor theme overrides the UI theme's editor colors)
  registerEditorThemeContributionConsumer,
  // terminalColorSchemes (Phase G; after themes)
  registerTerminalColorSchemeContributionConsumer,
  registerAiPromptContributionConsumer,
  registerCustomEditorContributionConsumer,
  // terminalProfiles (Phase D)
  registerTerminalProfileContributionConsumer,
  // fileIcons (Phase D)
  registerFileIconContributionConsumer,
  registerCommandSnippetContributionConsumer,
  registerTerminalLinkContributionConsumer,
  // httpCollections (Phase F)
  registerHttpCollectionContributionConsumer,
  // fileTemplates (Phase F)
  registerFileTemplateContributionConsumer,
  // walkthroughs (Phase F)
  registerWalkthroughContributionConsumer,
  // dashboardWidgets (Phase F)
  registerDashboardWidgetContributionConsumer,
  // searchProviders (Phase G)
  registerSearchProviderContributionConsumer,
  // aiContextProviders (Phase G)
  registerAiContextProviderContributionConsumer,
  // aiCatalogs: Settings → AI Discover sources
  registerAiCatalogContributionConsumer,
  registerAiProviderContributionConsumer,
  // httpCodeGenerators (Phase G)
  registerHttpCodeGeneratorContributionConsumer,
  // uriHandlers (Phase G)
  registerUriHandlerContributionConsumer,
  registerExternalOpenHandlerConsumer,
  // settingsPages: Settings → Plugins page list (parser loads on demand)
  registerSettingsPageContributionConsumer,
  // schedules: listed only; the Go host runs them
  registerScheduleContributionConsumer,
  // tasks (Phase G)
  registerTaskContributionConsumer,
  // languageServers (Phase G)
  registerLanguageServerContributionConsumer,
  // mcpServers
  registerMcpServerContributionConsumer,
  // acpAgents: listed only; started by reference after the user confirms
  registerAcpAgentContributionConsumer,
  // treeViews, testProviders, languageFeatures: on-demand providers
  registerTreeViewContributionConsumer,
  registerTestProviderContributionConsumer,
  registerLanguageFeatureContributionConsumer,
  // sessionSources, scmCommitMessage, fileDecorations: on-demand data providers (validation loads lazily)
  registerDataProviderContributionConsumers,
  // databaseDrivers (Phase E)
  registerDatabaseDriverContributionConsumer,
];

/**
 * Registers every built-in consumer and starts the sync. Browser preview has
 * no plugin host, so only built-in data (for example the MinCode keymap) is
 * available there. Returns a stop function that disposes every registration.
 */
export function startPluginContributionSync(): () => void {
  const unregister = consumers.map((register) => register());
  const stop = startContributionSync();
  return () => {
    stop();
    for (const dispose of unregister) dispose();
  };
}
