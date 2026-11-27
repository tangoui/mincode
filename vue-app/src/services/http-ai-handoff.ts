import { useVueI18n } from "@/i18n/messages";
import { sendToAi } from "@/services/ai-handoff";
import { isSensitiveDataName, redactSensitiveText, sanitizeUrlForAi } from "@/services/ai-tools/shared";

const { t } = useVueI18n();

function redactRequestUrl(raw: string, redact: (text: string) => string) {
  const sanitized = sanitizeUrlForAi(raw);
  try {
    const url = new URL(sanitized);
    url.hash = "";
    const params = new URLSearchParams();
    for (const [name, value] of url.searchParams) {
      params.append(redact(name), redact(value));
    }
    url.search = params.toString();
    return url.toString();
  } catch {
    return redact(sanitized);
  }
}

export function handoffHttpResponse(
  response: { status: number; status_text: string; headers: [string, string][]; body: string },
  request?: { method: string; requestUrl: string; secrets: string[] },
) {
  const redactKnownSecrets = (text: string) => redactSensitiveText((request?.secrets ?? [])
    .filter(Boolean)
    .reduce((value, secret) => value.split(secret).join("[REDACTED]"), text));
  const headers = response.headers
    .filter(([name]) => !isSensitiveDataName(name))
    .map(([name, value]) => `${name}: ${redactKnownSecrets(value)}`);
  const requestLine = request
    ? `${request.method} ${redactRequestUrl(request.requestUrl, redactKnownSecrets)}`
    : "";
  const status = `${response.status} ${redactKnownSecrets(response.status_text)}`.trim();
  return sendToAi({
    source: "http",
    title: t("ai.handoff.httpResponse"),
    prompt: t("ai.handoff.httpResponsePrompt"),
    content: [requestLine, [status, ...headers].join("\n"), redactKnownSecrets(response.body)].filter(Boolean).join("\n\n"),
  });
}
