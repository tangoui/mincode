<script setup lang="ts">
import { useVueI18n } from "@/i18n/messages";
import { browserLocalFeatures } from "@/services/browser-runtime/registry";
import { isBrowserLocalMode } from "@/services/desktop-bridge";

// 运行环境在页面生命周期内不会变化，挂载时判断一次即可。
const browserLocalMode = isBrowserLocalMode();
const { t } = useVueI18n();
</script>

<template>
  <el-popover
    v-if="browserLocalMode"
    placement="bottom-end"
    trigger="click"
    :width="340"
    popper-class="app-browser-local-popover"
  >
    <template #reference>
      <el-button
        text
        native-type="button"
        class="app-header-button app-browser-local-badge"
        :aria-label="t('browserLocal.badgeAria')"
        :title="t('browserLocal.badgeAria')"
      >
        <el-tag size="small" type="warning" effect="light" disable-transitions>
          {{ t("browserLocal.badge") }}
        </el-tag>
      </el-button>
    </template>

    <section class="app-browser-local" :aria-label="t('browserLocal.title')">
      <strong class="app-browser-local__title">{{ t("browserLocal.title") }}</strong>
      <p class="app-browser-local__text">{{ t("browserLocal.intro") }}</p>

      <h3 class="app-browser-local__heading">{{ t("browserLocal.featuresTitle") }}</h3>
      <ul v-if="browserLocalFeatures.length" class="app-browser-local__features">
        <li v-for="feature in browserLocalFeatures" :key="feature.id" class="app-browser-local__feature">
          <span class="app-browser-local__feature-name">{{ t(feature.titleKey) }}</span>
          <span class="app-browser-local__feature-limit">{{ t(feature.limitKey) }}</span>
        </li>
      </ul>
      <p v-else class="app-browser-local__text app-browser-local__text--muted">
        {{ t("browserLocal.featuresEmpty") }}
      </p>

      <el-alert
        class="app-browser-local__limits"
        type="warning"
        :closable="false"
        show-icon
        :title="t('browserLocal.limitations')"
      />
    </section>
  </el-popover>
</template>
