/** How a cloud catalog entry fits the running MinCode; derived from display-only catalog metadata. */
export interface MarketCompatibility {
  /** Version the install / upgrade button offers, or null when no published release runs on this MinCode. */
  offered: string | null;
  /** The latest release needs a different MinCode (`offered` may still be an older release). */
  latestUnsupported: boolean;
  /** Why the latest release does not fit: the running MinCode is too old or too new. */
  reason: "" | "host_too_old" | "host_too_new";
}

interface CatalogEntry {
  latest_published_version: string | null;
  host_compatibility: "" | "compatible" | "host_too_old" | "host_too_new";
  compatible_version: string | null;
}

export function marketCompatibility(entry: CatalogEntry): MarketCompatibility {
  const latest = entry.latest_published_version;
  if (!latest) return { offered: null, latestUnsupported: false, reason: "" };
  // No rating (no host range data) keeps the previous behaviour: offer the latest; installing still checks it.
  if (entry.host_compatibility === "" || entry.host_compatibility === "compatible") {
    return { offered: entry.compatible_version ?? latest, latestUnsupported: false, reason: "" };
  }
  return { offered: entry.compatible_version, latestUnsupported: true, reason: entry.host_compatibility };
}

/** i18n key and parameters describing a supported range (minimum inclusive, maximum exclusive). */
export function hostRangeMessage(min: string, max: string): { key: string; params: { min: string; max: string } } {
  const key = min && max ? "plugins.hostRange.both" : min ? "plugins.hostRange.min" : max ? "plugins.hostRange.max" : "plugins.hostRange.any";
  return { key, params: { min, max } };
}
