<script setup lang="ts">
import { computed, nextTick, reactive, ref, toRaw, watch } from "vue";
import { ElMessage, type FormInstance, type FormRules } from "element-plus";
import { parseDatabaseConnectionUrl } from "@/domain/database/connection-url";
import {
  extraSummary,
  initialOpenSections,
  tunnelSummary,
  type ConnectionFormSection,
} from "@/domain/database/connection-form-layout";
import { databaseTypeSearchKeywords } from "@/domain/database/type-picker-search";
import { loadSupportedDatabaseTypes } from "@/services/database-capabilities";
import {
  databaseDefaultPorts,
  databaseDriverInfo,
  databaseTypeLabels,
  type DatabaseConnection,
  type DatabaseTunnelConfig,
  type DbType,
} from "@/domain/database/types";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  chooseNativeFile,
  chooseNativeFileOrDirectory,
  isDesktopRuntime,
} from "@/services/desktop-bridge";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import DatabaseTypeIcon from "@/components/database/DatabaseTypeIcon.vue";
import {
  groupDatabaseTypeMatches,
  moveGridFocus,
  normalizeDatabaseTypeSearch,
  recentDatabaseTypes,
} from "@/domain/database/type-picker";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    initial?: DatabaseConnection;
    saving?: boolean;
    testing?: boolean;
    groupOptions?: string[];
    tagOptions?: string[];
  }>(),
  {
    groupOptions: () => [],
    tagOptions: () => [],
  },
);

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  save: [connection: DatabaseConnection];
  test: [connection: DatabaseConnection];
}>();

const { t } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();
const RECENT_CATEGORY_ID = "recent";
const formRef = ref<FormInstance>();
const step = ref<"picker" | "form">("picker");
const activeCategoryId = ref("rel");
const databaseTypeQuery = ref("");
const supportedDatabaseTypes = ref<Set<DbType> | null>(null);
const quickConnectionUrl = ref("");
/** Expanded folds of the form (tunnel / additional info); reset with the draft. */
const openSections = ref<ConnectionFormSection[]>([]);

const categories: Array<{ id: string; labelKey: MessageKey; types: DbType[] }> =
  [
    {
      id: "rel",
      labelKey: "database.category.relational",
      types: [
        "mysql",
        "mariadb",
        "postgres",
        "dolt",
        "sqlite",
        "duckdb",
        "sqlserver",
        "oracle",
        "firebird",
        "cockroachdb",
        "yugabyte",
        "greenplum",
        "questdb",
        "materialize",
        "risingwave",
      ],
    },
    {
      id: "domestic",
      labelKey: "database.category.domestic",
      types: [
        "dameng",
        "yashandb",
        "kingbase",
        "highgo",
        "vastbase",
        "opentenbase",
        "gaussdb",
        "opengauss",
        "oceanbase",
        "tidb",
        "polardb",
        "tdsql",
        "greatsql",
        "gbase",
        "gbase8t",
        "gbase8c",
        "goldendb",
        "starrocks",
        "doris",
      ],
    },
    {
      id: "nosql",
      labelKey: "database.category.nosql",
      types: ["mongodb", "redis", "cassandra", "neo4j", "couchdb"],
    },
    {
      id: "analytics",
      labelKey: "database.category.analytics",
      types: [
        "clickhouse",
        "tdengine",
        "influxdb",
        "elasticsearch",
        "opensearch",
        "meilisearch",
        "trino",
        "presto",
        "selectdb",
        "databend",
        "cloudberry",
        "redshift",
        "databricks",
        "bigquery",
        "dremio",
        "kylin",
        "pinot",
        "snowflake",
      ],
    },
    {
      id: "vector",
      labelKey: "database.category.vector",
      types: ["pgvector", "qdrant", "milvus", "weaviate", "pinecone", "chroma"],
    },
  ];

const draft = reactive<DatabaseConnection>({
  id: "",
  name: "",
  dbType: "mysql",
  host: "localhost",
  port: databaseDefaultPorts.mysql,
  username: "",
  password: "",
  database: "",
  filePath: "",
  token: "",
  org: "",
  authSource: "admin",
  ssl: false,
  savePassword: true,
  group: "",
  tags: [],
  description: "",
  favorite: false,
  createdAt: 0,
  updatedAt: 0,
});

const isTypeSupported = (dbType: DbType) => supportedDatabaseTypes.value?.has(dbType) ?? true;
const recentTypes = computed(() => recentDatabaseTypes(databaseStore.connections, isTypeSupported));
const availableCategories = computed(() => {
  const builtins = categories.filter((category) => category.types.some(isTypeSupported));
  const recent = recentTypes.value.length
    ? [{ id: RECENT_CATEGORY_ID, labelKey: "database.category.recent" as MessageKey, types: recentTypes.value }]
    : [];
  return [...recent, ...builtins];
});
const activeCategory = computed(
  () =>
    availableCategories.value.find((item) => item.id === activeCategoryId.value) ??
    availableCategories.value[0] ??
    categories[0]!,
);
/** 搜索时按分类分组显示（“最近使用”不参与，避免重复）；不搜索时只显示当前分类。 */
const databaseTypeGroups = computed(() => {
  const query = databaseTypeQuery.value;
  if (!normalizeDatabaseTypeSearch(query)) {
    return [{ id: activeCategory.value.id, labelKey: activeCategory.value.labelKey, types: activeCategory.value.types.filter(isTypeSupported) }];
  }
  return groupDatabaseTypeMatches(
    categories.map((category) => ({ ...category, types: category.types.filter(isTypeSupported) })),
    query,
    (dbType, category) =>
      [
        dbType,
        databaseTypeLabels[dbType],
        databaseDriverInfo[dbType].label,
        t((category as (typeof categories)[number]).labelKey),
        ...(databaseTypeSearchKeywords[dbType] ?? []),
      ].join(" "),
  ).map(({ category, types }) => ({ id: category.id, labelKey: (category as (typeof categories)[number]).labelKey, types }));
});
const visibleDatabaseTypes = computed(() => databaseTypeGroups.value.flatMap((group) => group.types));
const canConfirmBuiltinDriver = computed(() =>
  Boolean(draft.driver) && draft.dbType !== "plugin" && supportedDatabaseTypes.value?.has(draft.dbType) === true
);
const builtinDriverMissing = computed(() => Boolean(draft.driver) || draft.dbType === "plugin" || !isTypeSupported(draft.dbType));

function confirmBuiltinDriver() {
  if (!canConfirmBuiltinDriver.value) return;
  draft.driver = undefined;
  draft.options = undefined;
}
const fileDatabase = computed(() => draft.dbType === "sqlite" || draft.dbType === "duckdb");
const tokenPrimaryDatabase = computed(() =>
  [
    "influxdb",
    "qdrant",
    "weaviate",
    "pinecone",
    "databricks",
    "bigquery",
    "dremio",
    "snowflake",
  ].includes(draft.dbType),
);
const apiKeyDatabase = computed(() =>
  [
    "elasticsearch",
    "opensearch",
    "meilisearch",
    "milvus",
    "chroma",
    "kylin",
    "pinot",
  ].includes(draft.dbType),
);
const quickConnectionPlaceholder = computed(() =>
  t(`database.quickConnectPlaceholder.${draft.dbType}` as MessageKey),
);
const accessTokenFieldLabel = computed(() => {
  if (["qdrant", "weaviate", "pinecone"].includes(draft.dbType)) {
    return t("database.apiKeyShort");
  }
  if (draft.dbType === "bigquery") return t("database.oauthAccessToken");
  if (draft.dbType === "snowflake") return t("database.accessTokenOrJwt");
  return t("database.token");
});
const organizationDatabase = computed(() =>
  ["influxdb", "chroma", "databricks", "bigquery", "snowflake"].includes(
    draft.dbType,
  ),
);
const warehouseSchemaDatabase = computed(() =>
  ["databricks", "snowflake"].includes(draft.dbType),
);
const databaseFieldLabel = computed(() => {
  switch (draft.dbType) {
    case "databricks":
      return t("database.sqlWarehouseId");
    case "bigquery":
      return t("database.projectId");
    case "kylin":
      return t("database.project");
    case "dremio":
      return t("database.catalogOrSource");
    default:
      return firebirdDatabase.value
        ? t("database.firebirdDatabase")
        : oracleDatabase.value
          ? t("database.serviceName")
          : t("database.defaultDatabase");
  }
});
const organizationFieldLabel = computed(() => {
  switch (draft.dbType) {
    case "chroma":
      return t("database.chromaTenant");
    case "databricks":
      return t("database.catalog");
    case "bigquery":
      return t("database.location");
    case "snowflake":
      return t("database.warehouse");
    default:
      return t("database.org");
  }
});
const authSourceFieldLabel = computed(() =>
  warehouseSchemaDatabase.value ? t("database.schema") : t("database.authSource"),
);
const mongoDatabase = computed(() => draft.dbType === "mongodb");
const cassandraDatabase = computed(() => draft.dbType === "cassandra");
const oracleDatabase = computed(() => draft.dbType === "oracle");
const firebirdDatabase = computed(() => draft.dbType === "firebird");
const yashanDatabase = computed(() => draft.dbType === "yashandb");
const hasDatabase = computed(
  () =>
    ![
      "redis",
      "elasticsearch",
      "opensearch",
      "meilisearch",
      "qdrant",
      "weaviate",
    ].includes(draft.dbType),
);
const typeLabel = computed(() => databaseTypeLabels[draft.dbType]);
const dialogTitle = computed(() => {
  if (step.value === "picker") return t("database.pickTypeDialogTitle");
  return props.initial
    ? t("database.editConnectionWithType", { type: typeLabel.value })
    : t("database.newConnectionWithType", { type: typeLabel.value });
});
const tunnelSummaryText = computed(() => {
  const summary = tunnelSummary(draft.tunnel);
  if (!summary) return t("database.tunnelSummaryNone");
  return summary.host ? t("database.tunnelSummary", summary) : summary.type;
});
const EXTRA_SUMMARY_MAX_ITEMS = 3;
const extraSummaryText = computed(() => {
  const { items, hasDescription } = extraSummary(draft);
  if (items.length) {
    const shown = items
      .slice(0, EXTRA_SUMMARY_MAX_ITEMS)
      .join(t("pluginContrib.databaseDrivers.listSeparator"));
    const hidden = items.length - EXTRA_SUMMARY_MAX_ITEMS;
    return hidden > 0 ? `${shown} +${hidden}` : shown;
  }
  return hasDescription
    ? t("database.extraSummaryDescription")
    : t("database.extraSummaryEmpty");
});
const tunnelType = computed({
  get: () => draft.tunnel?.type ?? "none",
  set: (value: "none" | DatabaseTunnelConfig["type"]) => setTunnelType(value),
});

const rules: FormRules<DatabaseConnection> = {
  name: [
    { required: true, message: t("database.validation.name"), trigger: "blur" },
  ],
  host: [
    {
      validator: (_rule, value: string, callback) => {
        const required = !fileDatabase.value;
        if (required && !value.trim())
          callback(new Error(t("database.validation.host")));
        else callback();
      },
      trigger: "blur",
    },
  ],
  filePath: [
    {
      validator: (_rule, value: string, callback) => {
        const required = fileDatabase.value;
        if (required && !value.trim()) {
          callback(
            new Error(
              t("database.validation.file", {
                type: databaseTypeLabels[draft.dbType],
              }),
            ),
          );
        } else callback();
      },
      trigger: "blur",
    },
  ],
  database: [
    {
      validator: (_rule, value: string, callback) => {
        if (firebirdDatabase.value && !value.trim()) {
          callback(new Error(t("database.validation.firebirdDatabase")));
        } else callback();
      },
      trigger: "blur",
    },
  ],
};

// MySQL and PostgreSQL double as the entry for wire-compatible databases without a dedicated type.
const compatibleTypeHintKeys: Partial<Record<DbType, MessageKey>> = {
  mysql: "database.typeCompatHint.mysql",
  postgres: "database.typeCompatHint.postgres",
};


function defaultSslForDatabaseType(dbType: DbType) {
  return (
    databaseDefaultPorts[dbType] === 443 ||
    ["firebird", "pinecone", "redshift"].includes(dbType)
  );
}

function categoryForType(dbType: DbType) {
  return (
    categories.find((category) => category.types.includes(dbType))?.id ?? "rel"
  );
}

/** 新建连接默认打开“最近使用”；编辑或切换类型时定位到当前类型所在分类。 */
function defaultCategoryId() {
  if (!props.initial && step.value !== "form" && recentTypes.value.length) return RECENT_CATEGORY_ID;
  return categoryForType(draft.dbType);
}

const typeSearchField = ref<{ focus: () => void }>();
function focusTypeSearch() {
  if (step.value !== "picker") return;
  void nextTick(() => typeSearchField.value?.focus());
}

/** 选择页的连接串：识别出类型就直接进表单，识别不了时留在选择页并提示。 */
const pickerConnectionUrl = ref("");
const pickerConnectionUrlInvalid = ref(false);
function applyPickerConnectionUrl() {
  const value = pickerConnectionUrl.value.trim();
  pickerConnectionUrlInvalid.value = false;
  if (!value) return;
  const parsed = parseDatabaseConnectionUrl(value);
  if (!parsed || !isTypeSupported(parsed.dbType)) {
    pickerConnectionUrlInvalid.value = true;
    return;
  }
  draft.driver = undefined;
  draft.options = undefined;
  applyQuickConnectionUrl(value);
  pickerConnectionUrl.value = "";
}

/** 键盘：方向键在卡片间移动（按实际列数跨行），回车选择，Esc 交给弹窗关闭。 */
const typeGrid = ref<HTMLElement>();
const focusedTypeIndex = ref(-1);
watch([databaseTypeQuery, activeCategoryId], () => {
  focusedTypeIndex.value = normalizeDatabaseTypeSearch(databaseTypeQuery.value) ? 0 : -1;
});
function typeGridColumns() {
  const grid = typeGrid.value?.querySelector<HTMLElement>(".db-type-picker__grid");
  if (!grid) return 1;
  return Math.max(1, getComputedStyle(grid).gridTemplateColumns.split(" ").filter(Boolean).length);
}
function pickerCards() {
  return [...(typeGrid.value?.querySelectorAll<HTMLElement>(".db-type-card") ?? [])];
}
function handlePickerKeydown(event: KeyboardEvent) {
  if (event.isComposing) return;
  const cards = pickerCards();
  if (event.key === "Enter") {
    const card = cards[focusedTypeIndex.value];
    if (!card) return;
    event.preventDefault();
    card.click();
    return;
  }
  if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) return;
  // 搜索框里左右键用于移动光标，只有上下键交给卡片。
  const inSearch = (event.target as HTMLElement | null)?.closest(".db-type-picker__search");
  if (inSearch && (event.key === "ArrowLeft" || event.key === "ArrowRight")) return;
  event.preventDefault();
  focusedTypeIndex.value = moveGridFocus(focusedTypeIndex.value, cards.length, typeGridColumns(), event.key);
  cards[focusedTypeIndex.value]?.scrollIntoView({ block: "nearest" });
}

function selectCategory(categoryId: string) {
  databaseTypeQuery.value = "";
  activeCategoryId.value = categoryId;
}

function openTypePicker() {
  databaseTypeQuery.value = "";
  activeCategoryId.value = categoryForType(draft.dbType);
  step.value = "picker";
  focusTypeSearch();
}

function resetDraft(connection?: DatabaseConnection) {
  const now = Date.now();
  Object.assign(draft, {
    id: connection?.id ?? crypto.randomUUID(),
    name: connection?.name ?? "",
    dbType: connection?.dbType ?? "mysql",
    host: connection?.host ?? "localhost",
    port:
      connection?.port ?? databaseDefaultPorts[connection?.dbType ?? "mysql"],
    username: connection?.username ?? "",
    password: connection?.password ?? "",
    database: connection?.database ?? "",
    filePath: connection?.filePath ?? "",
    token: connection?.token ?? "",
    org: connection?.org ?? "",
    authSource: connection?.authSource ?? "admin",
    keyspace: connection?.keyspace ?? "",
    ssl:
      connection?.ssl ??
      defaultSslForDatabaseType(connection?.dbType ?? "mysql"),
    savePassword: connection?.savePassword ?? true,
    group: connection?.group ?? "",
    tags: [...(connection?.tags ?? [])],
    description: connection?.description ?? "",
    favorite: connection?.favorite ?? false,
    // toRaw: the connection comes from a reactive store and structuredClone cannot clone a proxy.
    tunnel: connection?.tunnel ? structuredClone(toRaw(connection.tunnel)) : undefined,
    driver: connection?.driver ? { ...connection.driver } : undefined,
    options:
      connection?.options && typeof connection.options === "object"
        ? { ...connection.options }
        : undefined,
    createdAt: connection?.createdAt ?? now,
    updatedAt: now,
  });
  openSections.value = connection ? initialOpenSections(connection) : [];
  activeCategoryId.value = defaultCategoryId();
  step.value = connection ? "form" : "picker";
  databaseTypeQuery.value = "";
  quickConnectionUrl.value = "";
  pickerConnectionUrl.value = "";
  pickerConnectionUrlInvalid.value = false;
}

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return;
    resetDraft(props.initial);
    void loadSupportedDatabaseTypes().then((types) => {
      supportedDatabaseTypes.value = types;
      if (!props.initial && types && !types.has(draft.dbType)) {
        pickDatabaseType(types.has("mysql") ? "mysql" : (types.values().next().value ?? "sqlite"));
      }
      activeCategoryId.value = defaultCategoryId();
      focusTypeSearch();
    });
  },
);

function pickDatabaseType(dbType: DbType) {
  const previousType = draft.dbType;
  const changed = previousType !== dbType;
  const shouldReplaceGeneratedName = isGeneratedName(previousType);
  // Picking a built-in card always means the built-in driver.
  draft.driver = undefined;
  draft.options = undefined;
  draft.dbType = dbType;
  if (changed) {
    draft.port = databaseDefaultPorts[dbType];
    draft.filePath = "";
    draft.tunnel = undefined;
    draft.ssl = defaultSslForDatabaseType(dbType);
    if (
      dbType === "pinecone" &&
      (!draft.host.trim() || draft.host === "localhost")
    ) {
      draft.host = "api.pinecone.io";
    } else if (
      dbType === "bigquery" &&
      (!draft.host.trim() || draft.host === "localhost")
    ) {
      draft.host = "bigquery.googleapis.com";
    } else if (
      previousType === "bigquery" &&
      draft.host === "bigquery.googleapis.com"
    ) {
      draft.host = "localhost";
    }
  }
  if (shouldReplaceGeneratedName) draft.name = databaseTypeLabels[dbType];
  step.value = "form";
}

function isGeneratedName(previousType: DbType) {
  return !draft.name.trim() || draft.name === databaseTypeLabels[previousType];
}

function applyQuickConnectionUrl(value: string) {
  quickConnectionUrl.value = value;
  const parsed = parseDatabaseConnectionUrl(value, draft.dbType);
  if (!parsed) return;

  const previousType = draft.dbType;
  const shouldReplaceGeneratedName =
    !draft.name.trim() || draft.name === databaseTypeLabels[previousType];
  Object.assign(draft, parsed, {
    filePath: "",
    tunnel: undefined,
  });
  if (shouldReplaceGeneratedName)
    draft.name = databaseTypeLabels[parsed.dbType];
  activeCategoryId.value = categoryForType(parsed.dbType);
  step.value = "form";
}

function setTunnelType(type: "none" | DatabaseTunnelConfig["type"]) {
  if (type === "none") {
    draft.tunnel = undefined;
  } else if (type === "ssh") {
    draft.tunnel = {
      type: "ssh",
      host: "",
      port: 22,
      username: "",
      authType: "password",
      password: "",
    };
  } else {
    draft.tunnel = {
      type,
      host: "",
      port: type === "socks5" ? 1080 : 8080,
      username: "",
      password: "",
    };
  }
}

async function validateDraft() {
  return Boolean(await formRef.value?.validate().catch(() => false));
}
function connectionPayload(): DatabaseConnection {
  const payload: DatabaseConnection = {
    ...draft,
    group: draft.group?.trim() || undefined,
    tags: [...(draft.tags ?? [])],
    tunnel: draft.tunnel ? structuredClone(toRaw(draft.tunnel)) : undefined,
  };
  return payload;
}

async function submit() {
  if (!(await validateDraft())) return;
  emit("save", connectionPayload());
}
async function testConnection() {
  if (!(await validateDraft())) return;
  if (builtinDriverMissing.value) {
    ElMessage.warning(t("database.driverUnavailable"));
    return;
  }
  emit("test", connectionPayload());
}
async function chooseDatabaseFile() {
  if (!isDesktopRuntime()) return;
  const pattern =
    draft.dbType === "duckdb" ? "*.duckdb;*.db" : "*.sqlite;*.sqlite3;*.db";
  const selected = await chooseNativeFile(t("database.chooseDatabaseFile"), [
    {
      DisplayName:
        draft.dbType === "duckdb"
          ? t("database.duckdbFile")
          : t("database.sqliteFile"),
      Pattern: pattern,
    },
    { DisplayName: t("database.allFiles"), Pattern: "*" },
  ]);
  if (selected) draft.filePath = selected;
}
async function choosePrivateKeyPath() {
  if (!isDesktopRuntime() || draft.tunnel?.type !== "ssh") return;
  const selected = await chooseNativeFileOrDirectory(
    t("database.choosePrivateKeyPath"),
  );
  if (selected) draft.tunnel.keyPath = selected;
}
function closeDialog() {
  emit("update:modelValue", false);
}
</script>

<template>
  <el-dialog
    append-to-body
    :model-value="modelValue"
    class="db-connection-dialog"
    width="min(960px, calc(100vw - 48px))"
    destroy-on-close
    :show-close="true"
    @update:model-value="emit('update:modelValue', $event)"
    @opened="focusTypeSearch"
  >
    <template #header>
      <div class="db-connection-dialog__header">
        <strong>{{ dialogTitle }}</strong>
      </div>
    </template>

    <div v-if="step === 'picker'" class="db-type-picker" @keydown="handlePickerKeydown">
      <div class="db-type-picker__intro">
        <AppSearchField
          ref="typeSearchField"
          v-model="databaseTypeQuery"
          class="db-type-picker__search"
          :placeholder="t('database.searchTypePlaceholderCount', { count: categories.reduce((sum, item) => sum + item.types.filter(isTypeSupported).length, 0) })"
          :aria-label="t('database.searchTypePlaceholder')"
          :clear-label="t('database.searchTypeClear')"
          clearable
        />
        <form class="db-type-picker__url" :class="{ 'is-invalid': pickerConnectionUrlInvalid }" @submit.prevent="applyPickerConnectionUrl">
          <label for="database-picker-url">{{ t("database.pasteConnectionUrl") }}</label>
          <el-input
            id="database-picker-url"
            v-model="pickerConnectionUrl"
            size="small"
            autocomplete="off"
            spellcheck="false"
            :placeholder="t('database.pasteConnectionUrlPlaceholder')"
            @input="pickerConnectionUrlInvalid = false"
            @paste="() => nextTick(applyPickerConnectionUrl)"
          />
          <el-button size="small" native-type="submit" :disabled="!pickerConnectionUrl.trim()">
            {{ t("database.recognizeConnectionUrl") }}
          </el-button>
          <span v-if="pickerConnectionUrlInvalid" class="db-type-picker__url-error" role="alert">
            {{ t("database.connectionUrlUnrecognized") }}
          </span>
        </form>
      </div>
      <div class="db-type-picker__body">
        <nav class="db-type-picker__categories" :aria-label="t('database.pickTypeTitle')">
          <template v-for="(category, index) in availableCategories" :key="category.id">
            <hr v-if="index > 0 && availableCategories[index - 1]?.id === RECENT_CATEGORY_ID" />
            <el-button
              text
              native-type="button"
              :class="{
                'is-active':
                  !databaseTypeQuery.trim() && activeCategory.id === category.id,
              }"
              :aria-current="!databaseTypeQuery.trim() && activeCategory.id === category.id ? 'true' : undefined"
              @click="selectCategory(category.id)"
            >
              <span>{{ t(category.labelKey) }}</span>
              <small>{{ category.types.filter(isTypeSupported).length }}</small>
            </el-button>
          </template>
        </nav>
        <el-scrollbar class="db-type-picker__scroll">
          <div ref="typeGrid" class="db-type-picker__scroll-content">
            <template v-if="visibleDatabaseTypes.length">
              <section
                v-for="group in databaseTypeGroups"
                :key="group.id"
                class="db-type-picker__group"
              >
                <h4>
                  {{ t(group.labelKey) }}
                  <small v-if="databaseTypeQuery.trim()">{{ group.types.length }}</small>
                </h4>
                <div class="db-type-picker__grid">
                  <button
                    v-for="dbType in group.types"
                    :key="dbType"
                    type="button"
                    class="db-type-card"
                    :class="{
                      'is-selected': draft.dbType === dbType && !draft.driver,
                      'is-focused': visibleDatabaseTypes.indexOf(dbType) === focusedTypeIndex,
                      'is-unavailable': !databaseDriverInfo[dbType].available,
                    }"
                    :title="
                      databaseDriverInfo[dbType].available
                        ? t('database.driverHint', { driver: databaseDriverInfo[dbType].label })
                        : t('database.driverUnavailableHint')
                    "
                    @click="pickDatabaseType(dbType)"
                  >
                    <DatabaseTypeIcon :db-type="dbType" class="db-type-card__logo" />
                    <span class="db-type-card__content">
                      <b>{{ databaseTypeLabels[dbType] }}</b>
                      <small>{{
                        !databaseDriverInfo[dbType].available
                          ? t("database.driverUnavailable")
                          : compatibleTypeHintKeys[dbType]
                            ? t(compatibleTypeHintKeys[dbType]!)
                            : t("database.driverProtocol", { driver: databaseDriverInfo[dbType].label })
                      }}</small>
                    </span>
                    <AppChevron class="db-type-card__arrow" direction="right" />
                  </button>
                </div>
              </section>
            </template>
            <el-empty
              v-else
              class="db-type-picker__empty"
              :description="t('database.searchTypeEmptyHint', { query: databaseTypeQuery.trim() })"
              :image-size="64"
            />
          </div>
        </el-scrollbar>
      </div>
      <div class="db-type-picker__footer">
        <span class="db-type-picker__keys">
          <kbd>↑</kbd><kbd>↓</kbd><kbd>←</kbd><kbd>→</kbd>{{ t("database.pickerKeySelect") }}
          <kbd>↵</kbd>{{ t("database.pickerKeyNext") }}
        </span>
        <span class="db-type-picker__actions">
          <el-button @click="closeDialog">{{ t("common.cancel") }}</el-button>
        </span>
      </div>
    </div>

    <div v-else class="db-connection-form-shell">
      <el-scrollbar class="db-connection-form-scroll">
        <el-form
          ref="formRef"
          :model="draft"
          :rules="rules"
          label-position="top"
          class="db-connection-form"
        >
          <div class="db-type-row">
            <div class="db-type-row__head">
              <DatabaseTypeIcon :db-type="draft.dbType" class="db-type-row__icon" />
              <div class="db-type-row__text">
                <strong>{{ typeLabel }}</strong>
                <p class="db-type-row__hint">
                  <span>{{
                    databaseDriverInfo[draft.dbType].available && !builtinDriverMissing
                      ? t("database.driverHint", { driver: databaseDriverInfo[draft.dbType].label })
                      : t("database.driverUnavailable")
                  }}</span>
                  <span v-if="compatibleTypeHintKeys[draft.dbType]">{{
                    t(compatibleTypeHintKeys[draft.dbType]!)
                  }}</span>
                </p>
              </div>
              <el-button size="small" @click="openTypePicker">
                {{
                  initial
                    ? t("database.changeType")
                    : t("database.previousStep")
                }}
              </el-button>
            </div>
            <el-alert v-if="canConfirmBuiltinDriver" type="warning" :closable="false" show-icon>
              <p>{{ t("database.confirmBuiltinDriverHint") }}</p>
              <el-button @click="confirmBuiltinDriver">{{ t("database.confirmBuiltinDriver") }}</el-button>
            </el-alert>
          </div>

          <section class="db-form-section db-form-main">
            <div class="db-basic-info-row">
              <el-form-item :label="t('database.connectionName')" prop="name">
                <el-input
                  v-model="draft.name"
                  :placeholder="t('database.connectionNamePlaceholder')"
                />
              </el-form-item>
              <el-button
                text
                class="db-favorite-btn"
                :class="{ 'is-favorite': draft.favorite }"
                @click="draft.favorite = !draft.favorite"
              >
                {{ draft.favorite ? "★" : "☆" }}
                {{
                  draft.favorite
                    ? t("database.removeFavorite")
                    : t("database.addFavorite")
                }}
              </el-button>
            </div>
            <div
              v-if="!fileDatabase && !draft.driver"
              class="db-quick-connect-section"
            >
              <label for="database-quick-connect">{{
                t("database.quickConnect")
              }}</label>
              <el-input
                id="database-quick-connect"
                class="db-quick-connect"
                :model-value="quickConnectionUrl"
                :placeholder="quickConnectionPlaceholder"
                autocomplete="off"
                spellcheck="false"
                @update:model-value="applyQuickConnectionUrl"
              >
                <template #prepend>URL</template>
              </el-input>
            </div>
            <el-alert
              v-if="yashanDatabase"
              :title="t('database.yashandbClientHint')"
              type="info"
              :closable="false"
              show-icon
            />
            <template v-if="fileDatabase">
              <el-form-item :label="t('database.filePath')" prop="filePath">
                <el-input
                  v-model="draft.filePath"
                  :placeholder="t('database.filePathPlaceholder')"
                >
                  <template #append
                    ><el-button
                      :disabled="!isDesktopRuntime()"
                      @click="chooseDatabaseFile"
                      >{{ t("database.chooseFile") }}</el-button
                    ></template
                  >
                </el-input>
              </el-form-item>
            </template>
            <template v-else>
              <div class="db-form-grid db-form-grid--host">
                <el-form-item :label="t('database.host')" prop="host"
                  ><el-input v-model="draft.host" placeholder="localhost"
                /></el-form-item>
                <el-form-item :label="t('database.port')"
                  ><el-input-number
                    v-model="draft.port"
                    :min="1"
                    :max="65535"
                    controls-position="right"
                /></el-form-item>
              </div>
              <div v-if="tokenPrimaryDatabase" class="db-form-grid">
                <el-form-item
                  :label="accessTokenFieldLabel"
                >
                  <el-input
                    v-model="draft.token"
                    type="password"
                    show-password
                  />
                </el-form-item>
              </div>
              <div v-else class="db-form-grid">
                <el-form-item :label="t('database.username')"
                  ><el-input v-model="draft.username"
                /></el-form-item>
                <el-form-item :label="t('database.password')">
                  <el-input
                    v-model="draft.password"
                    type="password"
                    show-password
                  />
                </el-form-item>
              </div>
              <div v-if="apiKeyDatabase" class="db-form-grid">
                <el-form-item :label="t('database.apiKey')">
                  <el-input
                    v-model="draft.token"
                    type="password"
                    show-password
                  />
                </el-form-item>
              </div>
              <div class="db-form-grid">
                <el-form-item
                  v-if="hasDatabase"
                  prop="database"
                  :label="databaseFieldLabel"
                >
                  <el-input
                    v-model="draft.database"
                    :placeholder="
                      firebirdDatabase
                        ? t('database.firebirdDatabasePlaceholder')
                        : oracleDatabase
                          ? t('database.serviceNamePlaceholder')
                          : undefined
                    "
                  />
                </el-form-item>
                <el-form-item
                  v-if="cassandraDatabase"
                  :label="t('database.keyspace')"
                  ><el-input
                    v-model="draft.keyspace"
                    :placeholder="t('database.keyspacePlaceholder')"
                /></el-form-item>
                <el-form-item
                  v-if="organizationDatabase"
                  :label="organizationFieldLabel"
                  ><el-input v-model="draft.org"
                /></el-form-item>
                <el-form-item
                  v-if="mongoDatabase || warehouseSchemaDatabase"
                  :label="authSourceFieldLabel"
                  ><el-input v-model="draft.authSource"
                /></el-form-item>
              </div>
            </template>
            <div class="db-check-row">
              <el-checkbox
                v-if="!fileDatabase && !yashanDatabase"
                v-model="draft.ssl"
                >{{
                  firebirdDatabase
                    ? t("database.wireEncryption")
                    : t("database.ssl")
                }}</el-checkbox
              >
              <el-checkbox v-model="draft.savePassword">{{
                t("database.savePassword")
              }}</el-checkbox>
            </div>
          </section>

          <!-- Rarely used blocks fold away; the header text (title + summary) is the
               accessible name of each fold's role=button header. -->
          <el-collapse v-model="openSections" class="db-form-folds">
            <el-collapse-item
              v-if="!fileDatabase && !draft.driver"
              name="tunnel"
              class="db-form-fold"
            >
              <template #title>
                <span class="db-form-fold__title">
                  <strong>{{ t("database.section.tunnel") }}</strong>
                  <span class="db-form-fold__summary">{{ tunnelSummaryText }}</span>
                </span>
              </template>
              <el-form-item :label="t('database.tunnelType')">
                <el-select v-model="tunnelType" class="w-full">
                  <el-option :label="t('database.tunnelNone')" value="none" />
                  <el-option label="SSH" value="ssh" />
                  <el-option label="SOCKS5" value="socks5" />
                  <el-option label="HTTP CONNECT" value="http_connect" />
                </el-select>
              </el-form-item>
              <template v-if="draft.tunnel">
                <div class="db-form-grid db-form-grid--host">
                  <el-form-item :label="t('database.proxyHost')"
                    ><el-input v-model="draft.tunnel.host"
                  /></el-form-item>
                  <el-form-item :label="t('database.port')"
                    ><el-input-number
                      v-model="draft.tunnel.port"
                      :min="1"
                      :max="65535"
                      controls-position="right"
                  /></el-form-item>
                </div>
                <div v-if="draft.tunnel.type === 'ssh'" class="db-form-grid">
                  <el-form-item :label="t('database.username')"
                    ><el-input v-model="draft.tunnel.username"
                  /></el-form-item>
                  <el-form-item :label="t('database.sshAuthType')">
                    <el-select v-model="draft.tunnel.authType"
                      ><el-option
                        :label="t('database.password')"
                        value="password" /><el-option
                        :label="t('database.privateKey')"
                        value="key"
                    /></el-select>
                  </el-form-item>
                  <el-form-item
                    v-if="draft.tunnel.authType === 'password'"
                    :label="t('database.password')"
                    ><el-input
                      v-model="draft.tunnel.password"
                      type="password"
                      show-password
                  /></el-form-item>
                  <el-form-item v-else :label="t('database.privateKeyPath')">
                    <el-input
                      v-model="draft.tunnel.keyPath"
                      placeholder="~/.ssh/id_rsa"
                    >
                      <template #append>
                        <el-button
                          :disabled="!isDesktopRuntime()"
                          :aria-label="t('database.choosePrivateKeyPath')"
                          @click="choosePrivateKeyPath"
                        >
                          {{ t("database.chooseFile") }}
                        </el-button>
                      </template>
                    </el-input>
                  </el-form-item>
                </div>
                <div v-else class="db-form-grid">
                  <el-form-item :label="t('database.username')"
                    ><el-input v-model="draft.tunnel.username"
                  /></el-form-item>
                  <el-form-item :label="t('database.password')"
                    ><el-input
                      v-model="draft.tunnel.password"
                      type="password"
                      show-password
                  /></el-form-item>
                </div>
              </template>
            </el-collapse-item>

            <el-collapse-item name="extra" class="db-form-fold">
              <template #title>
                <span class="db-form-fold__title">
                  <strong>{{ t("database.section.extra") }}</strong>
                  <span class="db-form-fold__summary">{{ extraSummaryText }}</span>
                </span>
              </template>
              <el-form-item :label="t('database.description')"
                ><el-input
                  v-model="draft.description"
                  type="textarea"
                  :rows="4"
                  :placeholder="t('database.descriptionPlaceholder')"
              /></el-form-item>
              <div class="db-form-grid">
                <el-form-item :label="t('database.group')">
                  <el-select
                    v-model="draft.group"
                    clearable
                    filterable
                    allow-create
                    default-first-option
                    :placeholder="t('database.groupPlaceholder')"
                  >
                    <el-option
                      v-for="group in groupOptions"
                      :key="group"
                      :label="group"
                      :value="group"
                    />
                  </el-select>
                </el-form-item>
                <el-form-item :label="t('database.tags')">
                  <el-select
                    v-model="draft.tags"
                    multiple
                    clearable
                    filterable
                    allow-create
                    default-first-option
                    :placeholder="t('database.tagsPlaceholder')"
                  >
                    <el-option
                      v-for="tag in tagOptions"
                      :key="tag"
                      :label="tag"
                      :value="tag"
                    />
                  </el-select>
                </el-form-item>
              </div>
            </el-collapse-item>
          </el-collapse>
        </el-form>
      </el-scrollbar>

      <footer class="db-connection-form__footer">
        <el-button
          :disabled="!isDesktopRuntime()"
          :loading="testing"
          class="is-test"
          @click="testConnection"
          >{{ t("database.testConnection") }}</el-button
        >
        <el-button class="is-cancel" @click="closeDialog">{{
          t("common.cancel")
        }}</el-button>
        <el-button type="primary" :loading="saving" @click="submit">{{
          t("common.save")
        }}</el-button>
      </footer>
    </div>
  </el-dialog>
</template>

<style scoped>
:global(.db-connection-dialog) {
  --el-dialog-padding-primary: 0;
  overflow: hidden;
  border-radius: 12px;
  background: var(--el-bg-color);
}
:global(.db-connection-dialog .el-dialog__header) {
  margin: 0;
  padding: 16px 22px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
:global(.db-connection-dialog .el-dialog__body) {
  padding: 0;
}
:global(.db-connection-dialog .el-dialog__headerbtn) {
  top: 10px;
  right: 12px;
}
.db-connection-dialog__header {
  display: flex;
  min-width: 0;
  align-items: center;
}
.db-connection-dialog__header strong {
  font-size: 15px;
}
.db-type-picker,
.db-connection-form-shell {
  display: flex;
  min-height: 0;
  height: min(72vh, 720px);
  flex-direction: column;
}
/* 选择页：搜索 + 粘贴连接串在顶部；左侧分类带数量；右侧按分组排列紧凑卡片（品牌色图标 + 名称 + 驱动一行）。 */
.db-type-picker__intro {
  display: grid;
  flex: none;
  gap: 8px;
  padding: 14px 20px 12px;
}
.db-type-picker__search {
  width: 100%;
  --app-search-field-height: 34px;
}
.db-type-picker__url {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 6px 8px 6px 12px;
  border: 1px dashed var(--app-border-strong);
  border-radius: 8px;
  background: var(--app-hover);
}
.db-type-picker__url label {
  flex: none;
  color: var(--app-text-regular);
  font-size: 12px;
  font-weight: 600;
}
.db-type-picker__url .el-input {
  min-width: 200px;
  flex: 1;
}
.db-type-picker__url :deep(.el-input__inner) {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11.5px;
}
.db-type-picker__url .el-button {
  margin: 0;
}
.db-type-picker__url.is-invalid {
  border-color: var(--el-color-danger);
}
.db-type-picker__url-error {
  width: 100%;
  color: var(--el-color-danger);
  font-size: 11.5px;
}
.db-type-picker__body {
  display: flex;
  min-height: 0;
  flex: 1;
  border-top: 1px solid var(--app-border);
}
.db-type-picker__categories {
  display: flex;
  width: 168px;
  flex: 0 0 168px;
  flex-direction: column;
  gap: 2px;
  padding: 10px 8px;
  overflow-y: auto;
  border-right: 1px solid var(--app-border);
}
.db-type-picker__categories hr {
  width: calc(100% - 8px);
  margin: 6px 4px;
  border: 0;
  border-top: 1px solid var(--app-border);
}
.db-type-picker__categories .el-button {
  display: flex;
  width: 100%;
  height: 32px;
  min-height: 32px;
  margin: 0;
  padding: 0 10px;
  border-radius: 6px;
  color: var(--app-text-regular);
  font-size: 12.5px;
}
.db-type-picker__categories .el-button :deep(> span) {
  display: flex;
  width: 100%;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.db-type-picker__categories .el-button small {
  color: var(--app-text-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}
.db-type-picker__categories .el-button:hover {
  background: var(--app-hover);
}
.db-type-picker__categories .el-button.is-active {
  background: var(--app-accent-soft);
  color: var(--app-accent);
  font-weight: 600;
}
.db-type-picker__scroll {
  min-width: 0;
  flex: 1;
}
.db-type-picker__scroll-content {
  display: grid;
  gap: 16px;
  padding: 12px 16px 16px;
}
.db-type-picker__group h4 {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 0 8px 2px;
  color: var(--app-text-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
}
.db-type-picker__group h4 small {
  font-weight: 400;
  font-variant-numeric: tabular-nums;
}
.db-type-picker__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 6px;
}
.db-type-card {
  position: relative;
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
  padding: 9px 26px 9px 10px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-surface);
  color: var(--app-text);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.12s, background-color 0.12s;
}
.db-type-card:hover,
.db-type-card.is-focused {
  border-color: color-mix(in srgb, var(--app-accent) 45%, var(--app-border));
  background: color-mix(in srgb, var(--app-accent-soft) 45%, var(--app-surface));
}
.db-type-card.is-selected {
  border-color: color-mix(in srgb, var(--app-accent) 60%, var(--app-border));
}
.db-type-card:focus-visible {
  outline: 2px solid color-mix(in srgb, var(--app-accent) 72%, var(--app-surface));
  outline-offset: 1px;
}
.db-type-card__logo {
  flex: none;
}
.db-type-card__logo--plugin {
  display: grid;
  width: 30px;
  height: 30px;
  place-items: center;
  overflow: hidden;
  border-radius: 7px;
  background: var(--app-accent-soft);
  color: var(--app-accent);
  font-size: 11px;
  font-weight: 700;
}
.db-type-card__logo--plugin img {
  width: 22px;
  height: 22px;
  object-fit: contain;
}
.db-type-card__content {
  display: grid;
  min-width: 0;
  line-height: 1.35;
}
.db-type-card__content b {
  overflow: hidden;
  font-size: 12.5px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-type-card__content small {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-type-card.is-unavailable .db-type-card__content small {
  color: var(--el-color-warning);
}
.db-type-card__arrow {
  position: absolute;
  top: 50%;
  right: 8px;
  color: var(--app-accent);
  opacity: 0;
  transform: translateY(-50%);
  transition: opacity 0.12s;
}
.db-type-card:hover .db-type-card__arrow,
.db-type-card.is-focused .db-type-card__arrow {
  opacity: 1;
}
.db-type-picker__empty {
  min-height: 260px;
}
.db-type-picker__footer {
  align-items: center;
  justify-content: space-between;
  border-top: 1px solid var(--app-border);
}
.db-type-picker__actions {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.db-type-picker__actions .el-button + .el-button {
  margin-left: 0;
}
.db-type-picker__jdbc-note {
  margin-left: 6px;
  color: var(--app-text-muted);
  font-size: 11px;
  font-weight: 400;
}
.db-type-picker__keys {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  color: var(--app-text-muted);
  font-size: 11.5px;
}
.db-type-picker__keys kbd {
  min-width: 18px;
  padding: 0 4px;
  border: 1px solid var(--app-border-strong);
  border-bottom-width: 2px;
  border-radius: 4px;
  color: var(--app-text-regular);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 10px;
  line-height: 15px;
  text-align: center;
}
.db-type-picker__keys kbd + kbd {
  margin-left: 1px;
}
.db-type-picker__keys kbd:nth-of-type(5) {
  margin-left: 12px;
}
@media (prefers-reduced-motion: reduce) {
  .db-type-card,
  .db-type-card__arrow {
    transition: none;
  }
}
.db-type-picker__footer,
.db-connection-form__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 13px 22px;
}
.db-connection-form-shell {
  /* The two-column grids fall back to one column by dialog width, not viewport width. */
  container: db-connection-form / inline-size;
}
.db-connection-form-scroll {
  min-height: 0;
  flex: 1;
}
.db-connection-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 14px 20px 20px;
}
/* Type row: icon, type name and one line of driver/provider text, then the driver select. */
.db-type-row {
  display: grid;
  gap: 8px;
  padding: 10px 12px;
  border: 1px solid var(--app-border);
  border-radius: 10px;
  background: var(--app-surface);
}
.db-type-row__head {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
}
.db-type-row__icon {
  flex: none;
}
.db-type-row__text {
  display: grid;
  min-width: 0;
  flex: 1;
  gap: 2px;
  line-height: 1.35;
}
.db-type-row__text strong {
  overflow: hidden;
  color: var(--app-text);
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-type-row__hint {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 0 12px;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 11.5px;
}
.db-type-row__text .db-driver-section__provider {
  margin: 0;
  font-size: 11.5px;
}
.db-type-row__head > .el-button {
  flex: none;
}
.db-type-row__driver {
  margin-bottom: 0;
}
.db-type-row__alerts {
  display: grid;
  gap: 8px;
}
.db-type-row__alerts:empty {
  display: none;
}
.db-type-row__alerts :deep(.el-alert__description p) {
  margin: 2px 0;
}
.db-type-row__alerts .db-driver-section__reconfirm {
  margin: 0;
}
.db-quick-connect-section {
  margin-bottom: 14px;
}
.db-quick-connect-section > label {
  display: block;
  margin-bottom: 6px;
  color: var(--app-text-regular);
  font-size: 12px;
}
.db-form-section {
  border: 1px solid var(--app-border);
  border-radius: 10px;
  background: var(--app-surface);
  padding: 12px 14px 4px;
}
.db-form-section > header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}
.db-form-section > header div {
  display: flex;
  align-items: center;
}
.db-form-section > header strong {
  font-size: 13px;
}
/* Folds (tunnel / additional info): one card like .db-form-section, items split by a
   border. The --el-collapse-* mapping is local because only this card needs it. */
.db-form-folds {
  --el-collapse-border-color: var(--app-border);
  --el-collapse-header-height: 38px;
  --el-collapse-header-bg-color: var(--app-surface);
  --el-collapse-header-text-color: var(--app-text);
  --el-collapse-content-bg-color: var(--app-surface);
  --el-collapse-content-text-color: var(--app-text);
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 10px;
  background: var(--app-surface);
}
.db-form-fold:last-child :deep(.el-collapse-item__header:not(.is-active)),
.db-form-fold:last-child :deep(.el-collapse-item__wrap) {
  border-bottom: 0;
}
.db-form-fold :deep(.el-collapse-item__header) {
  padding-left: 14px;
  padding-right: 12px;
}
.db-form-fold :deep(.el-collapse-item__header:focus-visible) {
  outline: 2px solid var(--app-accent);
  outline-offset: -2px;
}
.db-form-fold :deep(.el-collapse-item__title) {
  min-width: 0;
}
.db-form-fold :deep(.el-collapse-item__content) {
  padding: 4px 14px 4px;
}
.db-form-fold__title {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.db-form-fold__title strong {
  flex: none;
  font-size: 13px;
  font-weight: 600;
}
.db-form-fold__summary {
  overflow: hidden;
  min-width: 0;
  color: var(--app-text-muted);
  font-size: 12px;
  font-weight: 400;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}
.db-form-grid--host {
  grid-template-columns: minmax(0, 1fr) 160px;
}
.db-form-grid :deep(.el-input-number),
.db-form-grid :deep(.el-select) {
  width: 100%;
}
.db-basic-info-row {
  display: flex;
  align-items: end;
  gap: 10px;
  margin-bottom: 14px;
}
.db-basic-info-row :deep(.el-form-item) {
  flex: 1;
  margin-bottom: 0;
}
.db-favorite-btn {
  flex: none;
  border: 0;
  border-radius: 7px;
  background: transparent;
  padding: 6px 9px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  white-space: nowrap;
  font-size: 12px;
}
.db-favorite-btn:hover,
.db-favorite-btn.is-favorite {
  background: color-mix(in srgb, var(--app-warning) 13%, transparent);
  color: var(--app-warning);
}
.db-driver-section__provider {
  margin: 0 0 10px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
/* Credential notice: a short warning label at the end of the provider line. */
.db-driver-section__release-label {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  margin-left: 8px;
  color: var(--el-color-warning);
  white-space: nowrap;
  cursor: help;
  border-radius: 3px;
}
.db-driver-section__release-label:focus-visible {
  outline: 2px solid var(--el-color-warning);
  outline-offset: 2px;
}
.db-driver-section__reconfirm {
  margin: -4px 0 10px;
  color: var(--el-color-warning);
  font-size: 12px;
}
.db-driver-option__hint {
  display: block;
  width: 100%;
  color: var(--el-text-color-secondary);
  font-size: 11px;
  line-height: 1.4;
}
.db-check-row {
  display: flex;
  flex-wrap: wrap;
  gap: 18px;
  margin-bottom: 10px;
}
.db-check-row:empty {
  display: none;
}
.db-connection-form__footer {
  flex: 0 0 auto;
  border-top: 1px solid var(--el-border-color-lighter);
}
.db-connection-form__footer .is-test {
  --el-button-text-color: var(--app-accent);
  --el-button-bg-color: color-mix(in srgb, var(--app-accent) 9%, transparent);
  --el-button-border-color: color-mix(
    in srgb,
    var(--app-accent) 22%,
    transparent
  );
}
.db-connection-form__footer .is-test:hover {
  --el-button-text-color: var(--app-accent);
  --el-button-bg-color: color-mix(in srgb, var(--app-accent) 15%, transparent);
  --el-button-border-color: color-mix(
    in srgb,
    var(--app-accent) 35%,
    transparent
  );
}
.db-connection-form__footer .is-cancel {
  --el-button-text-color: var(--el-text-color-regular);
  --el-button-bg-color: var(--el-fill-color-light);
  --el-button-border-color: var(--el-border-color);
}
.db-connection-form__footer .is-cancel:hover {
  --el-button-text-color: var(--el-text-color-primary);
  --el-button-bg-color: var(--el-fill-color);
  --el-button-border-color: var(--el-border-color-dark);
}
@media (max-width: 720px) {
  .db-type-picker__categories {
    width: 126px;
    flex-basis: 126px;
  }
  .db-type-picker__keys {
    display: none;
  }
}
@container db-connection-form (max-width: 639px) {
  .db-basic-info-row {
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
  }
  .db-type-row__head {
    flex-wrap: wrap;
  }
  .db-form-grid,
  .db-form-grid--host {
    grid-template-columns: 1fr;
  }
}
</style>
