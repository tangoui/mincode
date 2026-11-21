// 一级侧栏（活动栏）文字标签显隐：模块级响应式单例，供 App.vue（宽度联动）
// 与 AppSidebar（标签渲染）共享；持久化到宿主持久存储，默认显示文字。
import { ref } from "vue";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

const LABELS_KEY = "app_sidebar_labels_v1";
const labelsVisible = ref(true);
let hydrated = false;

export function sidebarLabelsVisible() {
  return labelsVisible;
}

export async function hydrateSidebarLabels() {
  if (hydrated) return;
  hydrated = true;
  const stored = await loadPersistedValue<unknown>(LABELS_KEY);
  if (typeof stored === "boolean") labelsVisible.value = stored;
}

export function toggleSidebarLabels() {
  labelsVisible.value = !labelsVisible.value;
  void savePersistedValue(LABELS_KEY, labelsVisible.value);
}
