<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Lock } from "@element-plus/icons-vue";
import MinCodeMark from "@/assets/brand/mincode-mark.svg";
import { useVueI18n } from "@/i18n/messages";
import { useAppLockStore } from "@/stores/app-lock";
import { isolateBackground } from "@/composables/use-background-isolation";
import { setEmbeddedBrowserOverlayBlocked } from "@/services/browser-overlay-events";
import { hideEmbeddedBrowser } from "@/services/browser-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const { t } = useVueI18n();
const appLock = useAppLockStore();
const passcode = ref("");
const failed = ref(false);
const remainingSeconds = ref(0);
const passcodeInput = ref<{ focus: () => void } | null>(null);
const lockRoot = ref<HTMLElement | null>(null);
let countdownTimer: ReturnType<typeof setInterval> | undefined;
let releaseBackground: (() => void) | undefined;
const overlayBlockSource = "app-lock";

const waiting = computed(() => remainingSeconds.value > 0);
const submitDisabled = computed(() => !passcode.value || waiting.value || appLock.verifying);

function refreshCountdown() {
  const remaining = appLock.retryAvailableAt - Date.now();
  remainingSeconds.value = remaining > 0 ? Math.ceil(remaining / 1000) : 0;
}

async function submit() {
  if (submitDisabled.value) return;
  const unlocked = await appLock.unlock(passcode.value);
  if (unlocked) {
    passcode.value = "";
    failed.value = false;
    return;
  }
  failed.value = true;
  passcode.value = "";
  refreshCountdown();
  await nextTick();
  passcodeInput.value?.focus();
}

// 锁屏是全屏遮罩，但焦点仍可能停留在被遮住的控件上；每次进入锁屏都把焦点收回口令框。
watch(
  () => appLock.locked,
  async (isLocked) => {
    if (!isLocked) return;
    passcode.value = "";
    failed.value = false;
    await nextTick();
    passcodeInput.value?.focus();
  }
);

onMounted(async () => {
  // 遮罩只挡住视觉；不把背景设为 inert 时，Tab 仍能把焦点移到被遮住的终端等控件并执行输入。
  if (lockRoot.value) releaseBackground = isolateBackground(lockRoot.value);
  // 内嵌浏览器是叠在 WebView 之上的原生窗口，HTML 遮罩挡不住它；锁定时必须先隐藏。
  // 屏蔽源让浏览器页在解锁前保持隐藏；直接 hide 不依赖被遮挡窗口里会被节流的 rAF。
  setEmbeddedBrowserOverlayBlocked(overlayBlockSource, true);
  if (isDesktopRuntime()) void hideEmbeddedBrowser().catch(() => undefined);
  refreshCountdown();
  countdownTimer = setInterval(refreshCountdown, 1000);
  await nextTick();
  passcodeInput.value?.focus();
});

onBeforeUnmount(() => {
  releaseBackground?.();
  releaseBackground = undefined;
  setEmbeddedBrowserOverlayBlocked(overlayBlockSource, false);
  if (countdownTimer) clearInterval(countdownTimer);
});
</script>

<template>
  <Teleport to="body">
    <div
      ref="lockRoot"
      class="app-lock-screen"
      role="dialog"
      aria-modal="true"
      :aria-label="t('security.lock.title')"
    >
      <div class="app-lock-screen__card">
        <div class="app-lock-screen__brand">
          <img :src="MinCodeMark" alt="" />
        </div>
        <h1 class="app-lock-screen__title">{{ t("security.lock.title") }}</h1>
        <p class="app-lock-screen__hint">{{ t("security.lock.hint") }}</p>
        <el-input
          ref="passcodeInput"
          v-model="passcode"
          class="app-lock-screen__input"
          type="password"
          show-password
          autocomplete="current-password"
          :prefix-icon="Lock"
          :disabled="waiting || appLock.verifying"
          :placeholder="t('security.lock.placeholder')"
          :aria-label="t('security.lock.placeholder')"
          @keyup.enter="submit"
        />
        <el-alert
          v-if="waiting"
          class="app-lock-screen__alert"
          type="warning"
          show-icon
          :closable="false"
          :title="t('security.lock.throttled', { seconds: remainingSeconds })"
        />
        <el-alert
          v-else-if="failed"
          class="app-lock-screen__alert"
          type="error"
          show-icon
          :closable="false"
          :title="t('security.lock.wrongPasscode')"
        />
        <el-button
          class="app-lock-screen__submit"
          type="primary"
          :loading="appLock.verifying"
          :disabled="submitDisabled"
          @click="submit"
        >
          {{ t("security.lock.unlock") }}
        </el-button>
      </div>
    </div>
  </Teleport>
</template>
