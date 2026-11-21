<script setup lang="ts">
// Editor for the custom editor theme (Settings > General > Editor theme >
// Custom). Colors are picked, never typed as CSS; low-contrast colors are
// reported but allowed, since the theme is the user's own choice.
import { computed, ref } from "vue";
import { RefreshLeft } from "@element-plus/icons-vue";
import {
  CUSTOM_EDITOR_THEME_ALPHA_FIELDS,
  DEFAULT_CUSTOM_EDITOR_THEMES,
  customEditorThemeFrom,
  normalizeCustomEditorTheme,
  type CustomEditorTheme,
  type CustomEditorThemeField,
  type EditorThemeKind,
} from "@/domain/themes/editor-theme-data";
import { customEditorThemeLowContrast, editorThemeTitle, type EditorThemeRecord } from "@/domain/themes/editor-themes";
import { useVueI18n, type MessageKey } from "@/i18n/messages";

const props = defineProps<{ modelValue: boolean; theme: CustomEditorTheme; pluginThemes: readonly EditorThemeRecord[] }>();
const emit = defineEmits<{ "update:modelValue": [open: boolean]; save: [theme: CustomEditorTheme] }>();
const { t, localeChain } = useVueI18n();

const draft = ref<CustomEditorTheme>(normalizeCustomEditorTheme(props.theme));
const startFrom = ref("");

const EDITOR_FIELDS: CustomEditorThemeField[] = ["background", "foreground", "cursor", "selection", "lineHighlight", "lineNumber"];
const SYNTAX_FIELDS: CustomEditorThemeField[] = [
  "comment", "keyword", "string", "number", "constant", "function", "type", "variable", "operator", "tag", "attribute",
];

function fieldLabel(field: CustomEditorThemeField) {
  return t(`settings.editorTheme.field.${field}` as MessageKey);
}

const startOptions = computed(() => [
  { value: "default:dark", label: t("settings.editorTheme.defaultDark") },
  { value: "default:light", label: t("settings.editorTheme.defaultLight") },
  ...props.pluginThemes.map((theme) => ({ value: theme.id, label: editorThemeTitle(theme, localeChain.value) })),
]);

function applyStart(value: string) {
  startFrom.value = value;
  if (value === "default:dark" || value === "default:light") {
    draft.value = normalizeCustomEditorTheme(DEFAULT_CUSTOM_EDITOR_THEMES[value === "default:dark" ? "dark" : "light"]);
    return;
  }
  const theme = props.pluginThemes.find((item) => item.id === value);
  if (theme) draft.value = customEditorThemeFrom(theme);
}

function setKind(kind: EditorThemeKind) {
  draft.value = { kind, colors: { ...draft.value.colors } };
}

function setColor(field: CustomEditorThemeField, value: string | null) {
  if (!value) return;
  // Invalid values (for example alpha on an opaque field) keep the previous color.
  const next = normalizeCustomEditorTheme({ kind: draft.value.kind, colors: { ...draft.value.colors, [field]: value } });
  if (next.colors[field] !== value.toLowerCase()) next.colors[field] = draft.value.colors[field];
  draft.value = next;
}

function reset() {
  startFrom.value = "";
  draft.value = normalizeCustomEditorTheme(DEFAULT_CUSTOM_EDITOR_THEMES[draft.value.kind]);
}

const lowContrastText = computed(() =>
  customEditorThemeLowContrast(draft.value)
    .map((item) =>
      t("settings.editorTheme.lowContrastItem", { field: fieldLabel(item.field), ratio: item.ratio.toFixed(1), floor: item.floor.toFixed(1) })
    )
    .join(", ")
);

// Preview: a fixed code sample tokenized by hand. Each piece is [text, field].
type Piece = [string, CustomEditorThemeField | null];
const PREVIEW: Piece[][] = [
  [["// Sum the order lines", "comment"]],
  [["import", "keyword"], [" { ", "operator"], ["Order", "type"], [" } ", "operator"], ["from", "keyword"], [" ", null], ['"./order"', "string"], [";", "operator"]],
  [["const", "keyword"], [" ", null], ["TAX_RATE", "constant"], [" = ", "operator"], ["0.08", "number"], [";", "operator"]],
  [["export", "keyword"], [" ", null], ["function", "keyword"], [" ", null], ["total", "function"], ["(", "operator"], ["order", "variable"], [": ", "operator"], ["Order", "type"], ["): ", "operator"], ["number", "type"], [" {", "operator"]],
  [["  ", null], ["return", "keyword"], [" ", null], ["order", "variable"], [".", "operator"], ["lines", "variable"], [".", "operator"], ["reduce", "function"], ["((", "operator"], ["sum", "variable"], [", ", "operator"], ["line", "variable"], [") => ", "operator"], ["sum", "variable"], [" + ", "operator"], ["line", "variable"], [".", "operator"], ["price", "variable"], [", ", "operator"], ["0", "number"], [");", "operator"]],
  [["}", "operator"]],
  [["<", "operator"], ["PayButton", "tag"], [" ", null], ["label", "attribute"], ["=", "operator"], ['"Pay"', "string"], [" />", "operator"]],
];
const CURRENT_LINE = 3;
const SELECTED = { line: 4, piece: 5 };

function pieceStyle(field: CustomEditorThemeField | null, line: number, index: number) {
  const colors = draft.value.colors;
  const style: Record<string, string> = { color: field ? colors[field] : colors.foreground };
  if (field === "comment") style.fontStyle = "italic";
  if (line === SELECTED.line && index === SELECTED.piece) {
    style.backgroundColor = colors.selection;
    style.color = colors.foreground;
  }
  return style;
}

function save() {
  emit("save", normalizeCustomEditorTheme(draft.value));
}
</script>

<template>
  <el-dialog
    class="app-editor-theme-dialog"
    :model-value="modelValue"
    :title="t('settings.editorTheme.dialogTitle')"
    width="min(760px, calc(100vw - 40px))"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form label-position="left" label-width="auto" class="app-editor-theme-dialog__form">
      <div class="app-editor-theme-dialog__toolbar">
        <el-form-item :label="t('settings.editorTheme.kind')">
          <el-radio-group :model-value="draft.kind" @update:model-value="setKind($event as EditorThemeKind)">
            <el-radio-button value="dark">{{ t("settings.editorTheme.kindDark") }}</el-radio-button>
            <el-radio-button value="light">{{ t("settings.editorTheme.kindLight") }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('settings.editorTheme.startFrom')">
          <el-select
            :model-value="startFrom"
            :placeholder="t('settings.editorTheme.startFromPlaceholder')"
            @update:model-value="applyStart($event as string)"
          >
            <el-option v-for="option in startOptions" :key="option.value" :value="option.value" :label="option.label" />
          </el-select>
        </el-form-item>
      </div>

      <div class="app-editor-theme-dialog__sections">
        <section v-for="section in [{ key: 'sectionEditor', fields: EDITOR_FIELDS }, { key: 'sectionSyntax', fields: SYNTAX_FIELDS }]" :key="section.key">
          <div class="settings-section-label">{{ t(`settings.editorTheme.${section.key}` as MessageKey) }}</div>
          <div class="app-editor-theme-dialog__grid">
            <el-form-item v-for="field in section.fields" :key="field" :label="fieldLabel(field)">
              <el-color-picker
                :model-value="draft.colors[field]"
                :show-alpha="CUSTOM_EDITOR_THEME_ALPHA_FIELDS.has(field)"
                color-format="hex"
                :aria-label="fieldLabel(field)"
                @update:model-value="setColor(field, $event)"
              />
            </el-form-item>
          </div>
        </section>
      </div>
    </el-form>

    <div class="settings-section-label">{{ t("settings.editorTheme.preview") }}</div>
    <pre class="app-editor-theme-dialog__preview" :style="{ backgroundColor: draft.colors.background, color: draft.colors.foreground }" aria-hidden="true"><div
        v-for="(line, lineIndex) in PREVIEW"
        :key="lineIndex"
        class="app-editor-theme-dialog__line"
        :style="lineIndex === CURRENT_LINE ? { backgroundColor: draft.colors.lineHighlight } : undefined"
      ><span class="app-editor-theme-dialog__number" :style="{ color: lineIndex === CURRENT_LINE ? draft.colors.foreground : draft.colors.lineNumber }">{{ lineIndex + 1 }}</span><span
          v-for="(piece, index) in line"
          :key="index"
          :style="pieceStyle(piece[1], lineIndex, index)"
        >{{ piece[0] }}</span><span
          v-if="lineIndex === CURRENT_LINE"
          class="app-editor-theme-dialog__cursor"
          :style="{ backgroundColor: draft.colors.cursor }"
        /></div></pre>
    <el-alert
      v-if="lowContrastText"
      class="app-editor-theme-dialog__alert"
      type="warning"
      :closable="false"
      show-icon
      :title="t('settings.editorTheme.lowContrast', { items: lowContrastText })"
    />

    <template #footer>
      <el-button :icon="RefreshLeft" @click="reset">{{ t("settings.editorTheme.reset") }}</el-button>
      <el-button @click="emit('update:modelValue', false)">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" @click="save">{{ t("settings.editorTheme.save") }}</el-button>
    </template>
  </el-dialog>
</template>
