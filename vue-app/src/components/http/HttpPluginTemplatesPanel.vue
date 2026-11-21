<script setup lang="ts">
/**
 * Read-only list of HTTP collection templates contributed by plugins
 * (`httpCollections`). The panel never sends anything: "Import" and
 * "Open as draft" only emit, and the HTTP workspace copies the template into
 * the user's own collections or an unsaved tab.
 */
import { computed } from "vue";
import { Download, Document } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { resolvePluginTitle } from "@/domain/plugins/title";
import type {
  PluginHttpCollection,
  PluginHttpFolderTemplate,
  PluginHttpRequestTemplate,
} from "@/domain/http/plugin-collections";

const props = defineProps<{ templates: readonly PluginHttpCollection[] }>();
const emit = defineEmits<{
  import: [template: PluginHttpCollection];
  "open-draft": [request: PluginHttpRequestTemplate];
}>();

const { t, localeChain } = useVueI18n();

type TemplateRow =
  | { kind: "folder"; key: string; depth: number; name: string }
  | { kind: "request"; key: string; depth: number; request: PluginHttpRequestTemplate };

function flatten(folder: PluginHttpFolderTemplate, depth: number, prefix: string, out: TemplateRow[]) {
  folder.requests.forEach((request, index) => out.push({ kind: "request", key: `${prefix}r${index}`, depth, request }));
  folder.children.forEach((child, index) => {
    const key = `${prefix}f${index}`;
    out.push({ kind: "folder", key, depth, name: child.name });
    flatten(child, depth + 1, `${key}.`, out);
  });
  return out;
}

const items = computed(() =>
  props.templates.map((template) => ({
    template,
    title: resolvePluginTitle(template, localeChain.value),
    description: template.descriptionLocalized
      ? resolvePluginTitle({ title: template.description, titleLocalized: template.descriptionLocalized }, localeChain.value)
      : template.description,
    rows: flatten(template.collection, 0, "", []),
  }))
);
</script>

<template>
  <div class="http-plugin-templates">
    <p class="http-plugin-templates__hint">{{ t("pluginContrib.httpCollections.readOnly") }}</p>
    <article v-for="item in items" :key="item.template.key" class="http-plugin-template">
      <header class="http-plugin-template__header">
        <div class="http-plugin-template__title">
          <strong>{{ item.title }}</strong>
          <el-tag size="small" type="info" effect="plain">
            {{ t("pluginContrib.httpCollections.source", { name: item.template.pluginName }) }}
          </el-tag>
        </div>
        <el-button size="small" native-type="button" :icon="Download" @click="emit('import', item.template)">
          {{ t("pluginContrib.httpCollections.importAction") }}
        </el-button>
      </header>
      <p v-if="item.description" class="http-plugin-template__description">{{ item.description }}</p>
      <small class="http-plugin-template__counts">
        {{ t("pluginContrib.httpCollections.counts", { ...item.template.counts }) }}
      </small>
      <ul class="http-plugin-template__tree" role="list">
        <li
          v-for="row in item.rows"
          :key="row.key"
          :class="['http-plugin-template__row', `is-${row.kind}`]"
          :style="{ paddingInlineStart: `${row.depth * 12}px` }"
        >
          <template v-if="row.kind === 'folder'">
            <span class="http-plugin-template__folder">{{ row.name }}</span>
          </template>
          <template v-else>
            <div class="http-plugin-template__request">
              <span class="http-method">{{ row.request.method }}</span>
              <span class="http-plugin-template__name" :title="row.request.url">{{ row.request.name }}</span>
              <el-button
                text
                size="small"
                native-type="button"
                :icon="Document"
                :aria-label="t('pluginContrib.httpCollections.openDraft')"
                :title="t('pluginContrib.httpCollections.openDraft')"
                @click="emit('open-draft', row.request)"
              />
            </div>
            <code class="http-plugin-template__url">{{ row.request.url }}</code>
            <details v-if="row.request.documentation" class="http-plugin-template__docs">
              <summary>{{ t("pluginContrib.httpCollections.documentation") }}</summary>
              <pre>{{ row.request.documentation }}</pre>
            </details>
          </template>
        </li>
      </ul>
    </article>
    <div v-if="items.length === 0" class="http-sidebar-empty">{{ t("pluginContrib.httpCollections.empty") }}</div>
  </div>
</template>

<style scoped>
.http-plugin-templates {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 8px;
}

.http-plugin-templates__hint,
.http-plugin-template__description {
  margin: 0;
  color: var(--app-text-secondary);
  font-size: 12px;
}

.http-plugin-template {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
}

.http-plugin-template__header,
.http-plugin-template__title,
.http-plugin-template__request {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}

.http-plugin-template__header {
  justify-content: space-between;
}

.http-plugin-template__title {
  flex-wrap: wrap;
}

.http-plugin-template__counts {
  color: var(--app-text-secondary);
}

.http-plugin-template__tree {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.http-plugin-template__row {
  min-width: 0;
}

.http-plugin-template__folder {
  font-weight: 600;
  font-size: 12px;
}

.http-plugin-template__name {
  overflow: hidden;
  flex: 1;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.http-plugin-template__url {
  display: block;
  font-family: var(--app-font-mono);
  overflow: hidden;
  color: var(--app-text-secondary);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.http-plugin-template__docs pre {
  font-family: var(--app-font-mono);
  max-height: 160px;
  margin: 4px 0 0;
  overflow: auto;
  font-size: 11px;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
