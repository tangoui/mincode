<script setup lang="ts">
import { useVueI18n } from "@/i18n/messages";

defineProps<{ locale: "zh" | "en" }>();
const emit = defineEmits<{ "change-locale": [locale: "zh" | "en"] }>();
const { t } = useVueI18n();

</script>

<template>
  <div class="settings-flat-row">
    <strong>{{ t("settings.language") }}</strong>
    <el-select :model-value="locale" @change="emit('change-locale', $event as 'zh' | 'en')">
      <el-option value="zh" label="简体中文" />
      <el-option value="en" label="English" />
    </el-select>
  </div>
</template>
