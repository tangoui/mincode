export type AiTaskStepStatus = "pending" | "in_progress" | "completed" | "failed" | "cancelled";

export interface AiTaskStepPresentation {
  title: string;
  detail?: string;
  status: AiTaskStepStatus;
}

export interface AiSourcePresentation {
  title: string;
  url?: string;
  description?: string;
}

export interface AiArtifactPresentation {
  title: string;
  content: string;
  language?: string;
}

export interface AiEventPresentation {
  text: string;
  steps: AiTaskStepPresentation[];
  sources: AiSourcePresentation[];
  artifacts: AiArtifactPresentation[];
}

const TASK_STATUSES = new Set<AiTaskStepStatus>([
  "pending",
  "in_progress",
  "completed",
  "failed",
  "cancelled",
]);

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function text(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function taskStatus(value: unknown): AiTaskStepStatus {
  if (typeof value !== "string") return "pending";
  const normalized = value.trim().toLowerCase().replace(/-/g, "_");
  if (normalized === "running" || normalized === "active") return "in_progress";
  if (normalized === "done" || normalized === "success") return "completed";
  return TASK_STATUSES.has(normalized as AiTaskStepStatus)
    ? normalized as AiTaskStepStatus
    : "pending";
}

function parseSteps(value: unknown): AiTaskStepPresentation[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap<AiTaskStepPresentation>((item) => {
    if (typeof item === "string" && item.trim()) {
      return [{ title: item.trim(), status: "pending" }];
    }
    const candidate = record(item);
    const title = text(candidate?.title) ?? text(candidate?.content) ?? text(candidate?.name);
    if (!candidate || !title) return [];
    return [{ title, detail: text(candidate.detail), status: taskStatus(candidate.status) }];
  });
}

function parseSources(value: unknown): AiSourcePresentation[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (typeof item === "string" && item.trim()) return [{ title: item.trim() }];
    const candidate = record(item);
    const url = text(candidate?.url) ?? text(candidate?.href) ?? text(candidate?.uri);
    const title = text(candidate?.title) ?? text(candidate?.name) ?? url;
    if (!candidate || !title) return [];
    return [{ title, url, description: text(candidate.description) ?? text(candidate.snippet) }];
  });
}

function parseArtifacts(value: unknown): AiArtifactPresentation[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    const candidate = record(item);
    const title = text(candidate?.title) ?? text(candidate?.name) ?? text(candidate?.filename);
    const content = text(candidate?.content) ?? text(candidate?.text) ?? text(candidate?.data);
    if (!candidate || !title || content === undefined) return [];
    return [{ title, content, language: text(candidate.language) ?? text(candidate.type) }];
  });
}

export function safeExternalUrl(value?: string) {
  if (!value) return undefined;
  try {
    const parsed = new URL(value);
    if (parsed.username || parsed.password) return undefined;
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

/** Converts transport-escaped command output into readable text without touching escaped backslashes. */
export function normalizeAiEventText(value: string) {
  let normalized = value;
  // Native events can occasionally arrive as a JSON string instead of its decoded content.
  for (let index = 0; index < 2; index += 1) {
    const trimmed = normalized.trim();
    if (!trimmed.startsWith('"') || !trimmed.endsWith('"')) break;
    try {
      const parsed = JSON.parse(trimmed) as unknown;
      if (typeof parsed !== "string") break;
      normalized = parsed;
    } catch {
      break;
    }
  }
  return normalized
    .replace(/(?<!\\)\\r\\n/g, "\n")
    .replace(/(?<!\\)\\n/g, "\n")
    .replace(/(?<!\\)\\t/g, "\t");
}

export function parseAiEventPresentation(detail: string): AiEventPresentation {
  const normalizedDetail = normalizeAiEventText(detail);
  const fallback: AiEventPresentation = { text: normalizedDetail, steps: [], sources: [], artifacts: [] };
  if (!normalizedDetail.trim()) return fallback;
  try {
    const parsed = JSON.parse(normalizedDetail) as unknown;
    const candidate = record(parsed);
    if (!candidate) return fallback;
    const steps = parseSteps(candidate.steps ?? candidate.plan ?? candidate.tasks);
    const sources = parseSources(candidate.sources ?? candidate.citations ?? candidate.references);
    const artifacts = parseArtifacts(candidate.artifacts ?? candidate.outputs ?? candidate.files);
    if (!steps.length && !sources.length && !artifacts.length) return fallback;
    const presentationText = text(candidate.detail) ?? text(candidate.summary) ?? text(candidate.message) ?? "";
    return {
      text: normalizeAiEventText(presentationText),
      steps,
      sources,
      artifacts,
    };
  } catch {
    return fallback;
  }
}
