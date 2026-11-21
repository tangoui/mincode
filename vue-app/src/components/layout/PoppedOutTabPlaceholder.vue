<script setup lang="ts">
import { Connection, RefreshLeft } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { closePopupWindow, focusPopupWindow } from "@/services/popup-tab";

const props = defineProps<{ label: string }>();
const { t } = useVueI18n();

async function focusWindow() {
  try {
    await focusPopupWindow(props.label);
  } catch {
    ElMessage.warning(t("tab.popupUnavailable"));
  }
}

async function restoreHere() {
  try {
    // 等待 native WindowClosing 事件回传给主窗口后再恢复，避免旧终端布局抢先挂载。
    await closePopupWindow(props.label);
  } catch {
    ElMessage.warning(t("tab.popupUnavailable"));
  }
}
</script>

<template>
  <section class="popped-out-tab-placeholder">
    <el-icon><Connection /></el-icon>
    <strong>{{ t("tab.poppedOutTitle") }}</strong>
    <p>{{ t("tab.poppedOutHint") }}</p>
    <div>
      <el-button @click="focusWindow">{{ t("tab.focusPoppedOut") }}</el-button>
      <el-button type="primary" :icon="RefreshLeft" @click="restoreHere">
        {{ t("tab.poppedOutReclaim") }}
      </el-button>
    </div>
  </section>
</template>
