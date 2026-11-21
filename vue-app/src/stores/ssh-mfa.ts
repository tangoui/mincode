import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { generateTotp } from "@/domain/ssh/totp";
import type {
  SshMfaPromptItem,
  SshMfaPromptPayload,
  SshRuntimeCredentials,
} from "@/domain/ssh/types";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

const OTP_PROMPT_PATTERN =
  /(?:\botp\b|\btotp\b|\bmfa\b|\b2fa\b|one[- ]?time|verification|verify|authenticator|passcode|\btoken\b|\bcode\b|验证码|动态口令|动态密码|令牌)/i;
const PASSWORD_PROMPT_PATTERN = /password|密码|口令/i;
const NON_PASSWORD_PROMPT_PATTERN = /passcode|code|otp|token|验证码|动态/i;

type SshMfaCredentials = Pick<SshRuntimeCredentials, "password" | "totpUri">;

/**
 * Returns whether the prompt requests a one-time code.
 *
 * SSH servers may mark an MFA code prompt as echoable, so `echo` cannot decide
 * which visual control to render. The prompt content is the reliable signal.
 */
export function isSshOtpPrompt(prompt: SshMfaPromptItem) {
  return OTP_PROMPT_PATTERN.test(prompt.prompt);
}

/** Only password-specific questions use a saved password. */
export function isSshPasswordPrompt(prompt: SshMfaPromptItem) {
  return PASSWORD_PROMPT_PATTERN.test(prompt.prompt) && !NON_PASSWORD_PROMPT_PATTERN.test(prompt.prompt);
}

/**
 * Builds transient values for the MFA dialog fallback.
 * A native SSH callback normally submits a standalone TOTP challenge directly;
 * this path preserves the generated code when the callback falls back to the MFA dialog.
 */
export async function buildSshMfaInitialResponses(
  prompts: SshMfaPromptItem[],
  credentials?: SshMfaCredentials
): Promise<string[]> {
  const password = credentials?.password?.trim();
  let code = "";
  if (credentials?.totpUri?.trim() && prompts.some(isSshOtpPrompt)) {
    try {
      code = await generateTotp(credentials.totpUri);
    } catch {
      // The dialog remains usable for manual input. Never expose authentication secrets or codes.
    }
  }

  return prompts.map((prompt) => {
    if (password && isSshPasswordPrompt(prompt)) return password;
    if (code && isSshOtpPrompt(prompt)) return code;
    return "";
  });
}

export const useSshMfaStore = defineStore("ssh-mfa", () => {
  const queue = ref<SshMfaPromptPayload[]>([]);
  const submitting = ref(false);
  const credentialsBySessionId = new Map<string, SshMfaCredentials>();
  let stopListening: (() => void) | null = null;

  const current = computed(() => queue.value[0] ?? null);

  function ensureListening() {
    if (stopListening || !isDesktopRuntime()) return;
    stopListening = listenAppEvent<SshMfaPromptPayload>("ssh-mfa-prompt", (payload) => {
      void handlePrompt(payload);
    });
  }

  function registerCredentials(sessionId: string, credentials?: SshMfaCredentials) {
    const password = credentials?.password?.trim();
    const totpUri = credentials?.totpUri?.trim();
    if (!password && !totpUri) {
      credentialsBySessionId.delete(sessionId);
      return;
    }
    credentialsBySessionId.set(sessionId, {
      ...(password ? { password } : {}),
      ...(totpUri ? { totpUri } : {}),
    });
  }

  /** @deprecated Use registerCredentials so password and TOTP fallbacks stay aligned. */
  function registerTotpUri(sessionId: string, totpUri?: string) {
    registerCredentials(sessionId, { totpUri });
  }

  function unregisterSession(sessionId: string) {
    credentialsBySessionId.delete(sessionId);
    queue.value = queue.value.filter((prompt) => prompt.sessionId !== sessionId);
  }

  async function handlePrompt(payload: SshMfaPromptPayload) {
    if (!payload?.sessionId || !Array.isArray(payload.prompts)) return;

    // Keep a prefilled dialog as the visible fallback when a server asks several
    // interactive questions or a native auto-response is skipped.
    const initialResponses = await buildSshMfaInitialResponses(
      payload.prompts,
      credentialsBySessionId.get(payload.sessionId)
    );
    queue.value.push({ ...payload, initialResponses });
  }

  async function respond(responses: string[]) {
    const prompt = current.value;
    if (!prompt || submitting.value) return;
    submitting.value = true;
    try {
      await respondPrompt(prompt, responses);
    } finally {
      const index = queue.value.findIndex((item) => item === prompt);
      if (index >= 0) queue.value.splice(index, 1);
      submitting.value = false;
    }
  }

  async function respondPrompt(prompt: SshMfaPromptPayload, responses: string[]) {
    await invokeBackend<void>("respond_ssh_mfa", {
      payload: { sessionId: prompt.sessionId, responses },
    });
  }

  function dispose() {
    stopListening?.();
    stopListening = null;
    queue.value = [];
    credentialsBySessionId.clear();
  }

  return {
    current,
    submitting,
    ensureListening,
    registerCredentials,
    registerTotpUri,
    unregisterSession,
    respond,
    dispose,
  };
});

export function shouldAutoRespond(payload: SshMfaPromptPayload) {
  const [prompt] = payload.prompts;
  // The Go callback applies the direct automatic response. This predicate
  // remains the shared eligibility rule for its frontend fallback tests.
  return Boolean(prompt && payload.prompts.length === 1 && isSshOtpPrompt(prompt));
}

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useSshMfaStore, import.meta.hot));
}
