/**
 * Runtime of the data provider consumers (services/contributions/data-providers.ts):
 * validation and the Pinia store load only when a snapshot has entries of a
 * data provider point, keeping both out of the entry chunk. Each point has
 * one applier; the entry module calls it with every later snapshot of that
 * point (an empty list clears it).
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { collectDecorationProviders } from "@/domain/plugins/file-decorations";
import { collectScmProviders } from "@/domain/plugins/scm-commit-message";
import { collectSessionSources } from "@/domain/plugins/session-sources";
import { usePluginDataProvidersStore } from "@/stores/plugin-data-providers";

export type DataProviderApplier = (contributions: readonly PointContribution[]) => void;

export const dataProviderAppliers: Readonly<Partial<Record<string, DataProviderApplier>>> = {
  sessionSources: (contributions) => usePluginDataProvidersStore().setSessionSources(collectSessionSources(contributions)),
  scmCommitMessage: (contributions) => usePluginDataProvidersStore().setScmProviders(collectScmProviders(contributions)),
  fileDecorations: (contributions) => usePluginDataProvidersStore().setDecorationProviders(collectDecorationProviders(contributions)),
};
