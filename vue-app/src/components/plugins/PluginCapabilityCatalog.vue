<script setup lang="ts">
import { onBeforeUnmount } from "vue";
import { createCapabilityRegistry, type AiCapability, type AiCapabilityRisk } from "@/domain/ai/capability-registry";
import { createResourceRegistry } from "@/domain/ai/resource-registry";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { registerNativeAiCapabilities } from "@/services/ai-native-tools";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const { t } = useVueI18n();
const capabilityRegistry = createCapabilityRegistry();
const resourceRegistry = createResourceRegistry();
const unregisterCapabilities = registerNativeAiCapabilities({
  nativeRuntime: isDesktopRuntime(),
  capabilities: capabilityRegistry,
  registry: resourceRegistry,
  invoke: invokeBackend,
  listResources: (capability) => resourceRegistry.list(capability),
});
const capabilities = capabilityRegistry.listCapabilities();
const groupedCapabilities = capabilities.reduce<Record<string, AiCapability[]>>((groups, capability) => {
  (groups[capability.module] ??= []).push(capability);
  return groups;
}, {});

function riskType(risk: AiCapabilityRisk) {
  if (risk === "dangerous") return "danger";
  if (risk === "read") return "success";
  return "warning";
}

function riskLabel(risk: AiCapabilityRisk) {
  return t(`ai.agentic.risk.${risk}` as MessageKey);
}

onBeforeUnmount(unregisterCapabilities);
</script>

<template>
  <el-scrollbar class="plugin-manager-capability-scroll">
    <main class="plugin-manager-capability-grid">
      <div class="plugin-manager-capability-intro">
        <strong>{{ t("plugins.capabilityCatalog") }}</strong>
        <span>{{ t("plugins.capabilityCount", { count: capabilities.length }) }}</span>
      </div>
      <section
        v-for="(items, module) in groupedCapabilities"
        :key="module"
        class="plugin-manager-capability-group"
      >
        <h3>{{ module }}</h3>
        <article v-for="item in items" :key="item.tool.name">
          <div>
            <strong>{{ item.tool.name }}</strong>
            <p>{{ item.tool.description }}</p>
          </div>
          <el-tag size="small" :type="riskType(item.risk)">
            {{ riskLabel(item.risk) }}
          </el-tag>
        </article>
      </section>
    </main>
  </el-scrollbar>
</template>
