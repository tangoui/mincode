// Skills installed from Tencent SkillHub before Discover stopped searching it
// keep their stored documentation links; this maps the old API and legacy host
// links to the public skill page.
const TENCENT_SKILLHUB_WEB_BASE_URL = "https://skillhub.cn/";

function validHttpUrl(value: unknown) {
  try {
    const url = new URL(typeof value === "string" ? value.trim() : "");
    return url.protocol === "https:" || url.protocol === "http:" ? url.toString() : "";
  } catch {
    return "";
  }
}

/** Maps API and legacy Tencent SkillHub links to the public skill detail route. */
export function normalizeTencentSkillHubDocsUrl(value: unknown) {
  const publishedUrl = validHttpUrl(value);
  if (!publishedUrl) return "";
  const url = new URL(publishedUrl);
  const legacyHosts = ["api.skillhub.cn", "skillhub.cloud.tencent.com", "skillhub.tencent.com"];
  if (!legacyHosts.includes(url.hostname)) return url.toString();

  const segments = url.pathname.split("/").filter(Boolean);
  if (segments.length !== 2) return "";
  return new URL(
    `/skills/${encodeURIComponent(segments[0]!)}/${encodeURIComponent(segments[1]!)}`,
    TENCENT_SKILLHUB_WEB_BASE_URL
  ).toString();
}
