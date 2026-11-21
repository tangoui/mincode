<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { InfoFilled, Key } from "@element-plus/icons-vue";
import { ElInputOtp, type InputOtpInstance } from "element-plus";
import "element-plus/es/components/input-otp/style/css";
import { storeToRefs } from "pinia";
import type { SshMfaPromptPayload } from "@/domain/ssh/types";
import { useVueI18n } from "@/i18n/messages";
import { isSshOtpPrompt, useSshMfaStore } from "@/stores/ssh-mfa";

const DEFAULT_OTP_LENGTH = 6;
const OTP_LENGTH_PATTERN = /(?:^|\D)((?:10|[4-9]))\s*(?:[- ]?(?:digit|digits|character|characters)|位)/i;

const mfaStore = useSshMfaStore();
const { current, submitting } = storeToRefs(mfaStore);
const { t } = useVueI18n();
const responses = ref<string[]>([]);
const otpInput = ref<InputOtpInstance | InputOtpInstance[] | null>(null);

const visible = computed(() => current.value !== null);
const promptName = computed(() => {
  const name = current.value?.name?.trim();
  return name && name !== t("ssh.mfa.title") ? name : "";
});
const isOtpChallenge = computed(() => isOtpChallengePrompt(current.value));
const otpLength = computed(() => {
  const prompt = current.value?.prompts[0]?.prompt ?? "";
  return getOtpLength(prompt);
});
const canSubmit = computed(() => {
  if (!current.value || submitting.value) return false;
  return !isOtpChallenge.value || responses.value[0]?.length === otpLength.value;
});

watch(
  current,
  async (prompt) => {
    responses.value = prompt
      ? prompt.prompts.map((_, index) => prompt.initialResponses?.[index] ?? "")
      : [];
    if (isOtpChallengePrompt(prompt)) {
      await nextTick();
      focusOtpInput();
    }
  },
  { immediate: true },
);

function focusOtpInput() {
  const input = Array.isArray(otpInput.value) ? otpInput.value[0] : otpInput.value;
  input?.focus();
}

function submit() {
  if (!canSubmit.value) return;
  void mfaStore.respond([...responses.value]);
}

function submitOtp(code: string) {
  if (!isOtpChallenge.value || submitting.value || code.length !== otpLength.value) return;
  responses.value = [code];
  void mfaStore.respond([code]);
}

function cancel() {
  if (!current.value) return;
  void mfaStore.respond(current.value.prompts.map(() => ""));
}

function isOtpChallengePrompt(prompt: SshMfaPromptPayload | null) {
  const [mfaPrompt] = prompt?.prompts ?? [];
  return Boolean(prompt && prompt.prompts.length === 1 && mfaPrompt && isSshOtpPrompt(mfaPrompt));
}

function getOtpLength(prompt: string) {
  const length = OTP_LENGTH_PATTERN.exec(prompt)?.[1];
  return length ? Number(length) : DEFAULT_OTP_LENGTH;
}
</script>

<template>
  <el-dialog
    :model-value="visible"
    class="ssh-mfa-dialog"
    width="420px"
    align-center
    append-to-body
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    :show-close="false"
  >
    <template #header>
      <div class="ssh-mfa-dialog__heading">
        <span class="ssh-mfa-dialog__heading-icon" aria-hidden="true"><el-icon><Key /></el-icon></span>
        <span>
          <strong>{{ t("ssh.mfa.title") }}</strong>
          <small v-if="promptName">{{ promptName }}</small>
        </span>
      </div>
    </template>

    <template v-if="current">
      <div v-if="current.instructions" class="ssh-mfa-dialog__instructions">
        <el-icon aria-hidden="true"><InfoFilled /></el-icon>
        <p>{{ current.instructions }}</p>
      </div>

      <el-form class="ssh-mfa-dialog__form" label-position="top" @submit.prevent="submit">
        <el-form-item
          v-for="(prompt, index) in current.prompts"
          :key="`${current.sessionId}-${index}`"
          :label="prompt.prompt || t('ssh.mfa.response')"
        >
          <ElInputOtp
            v-if="isOtpChallenge && index === 0"
            ref="otpInput"
            v-model="responses[0]"
            class="ssh-mfa-dialog__otp"
            :length="otpLength"
            inputmode="numeric"
            :disabled="submitting"
            @finish="submitOtp"
          />
          <el-input
            v-else
            v-model="responses[index]"
            :type="prompt.echo ? 'text' : 'password'"
            :show-password="!prompt.echo"
            autocomplete="one-time-code"
            @keyup.enter="submit"
          />
        </el-form-item>
      </el-form>
    </template>

    <template #footer>
      <el-button size="small" :disabled="submitting" @click="cancel">{{ t("common.cancel") }}</el-button>
      <el-button size="small" type="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
        {{ t("ssh.mfa.submit") }}
      </el-button>
    </template>
  </el-dialog>
</template>
