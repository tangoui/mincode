/**
 * Databases that official plugins can add to the "New connection" picker.
 *
 * The picker lists drivers of installed, enabled plugins from the driver
 * catalog store. This list covers the rest: when a search matches a database
 * whose plugin is not installed, the picker offers a "provided by <plugin>"
 * card that opens the plugin manager. Nothing is installed or bound
 * automatically.
 *
 * `scripts/plugin-driver-suggestions.test.mjs` keeps the entries in step with
 * every `official-plugins/*\/plugin.json` `databaseDrivers` entry (plugin id,
 * driver id, title, titleLocalized, plugin name, replaces).
 */
import type { DbType } from "./types.ts";
import { normalizeDatabaseTypeSearch } from "./type-picker.ts";

export interface PluginDriverSuggestion {
  pluginId: string;
  /** Manifest `name` of the plugin, shown as "provided by". */
  pluginName: string;
  driverId: string;
  title: string;
  titleLocalized?: Record<string, string>;
  /** Built-in type the plugin driver stands in for; hidden when that built-in is available. */
  replaces?: DbType;
  /** Extra search terms (vendor names, Chinese names, abbreviations). */
  keywords: readonly string[];
  /** The vendor JDBC jar must be registered on the plugin's management page first. */
  requiresDriverJar?: boolean;
}

export interface PluginDriverSuggestionMatch {
  suggestion: PluginDriverSuggestion;
}

const JDBC_BRIDGE = { pluginId: "jdbc-bridge", pluginName: "JDBC 桥接 / JDBC Bridge" } as const;

export const PLUGIN_DRIVER_SUGGESTIONS: readonly PluginDriverSuggestion[] = [
  // jdbc-bridge: dedicated drivers
  { ...JDBC_BRIDGE, driverId: "teradata", title: "Teradata", titleLocalized: { zh: "Teradata" }, keywords: ["Teradata", "jdbc"] },
  { ...JDBC_BRIDGE, driverId: "informix", title: "IBM Informix", titleLocalized: { zh: "IBM Informix" }, keywords: ["Informix", "IBM", "GBase 8s", "jdbc"] },
  { ...JDBC_BRIDGE, driverId: "access", title: "Microsoft Access", titleLocalized: { zh: "Microsoft Access" }, keywords: ["Access", "mdb", "accdb", "UCanAccess", "jdbc"] },
  { ...JDBC_BRIDGE, driverId: "h2", title: "H2", titleLocalized: { zh: "H2" }, keywords: ["H2", "H2 Database", "jdbc"] },
  { ...JDBC_BRIDGE, driverId: "ignite", title: "Apache Ignite", titleLocalized: { zh: "Apache Ignite" }, keywords: ["Ignite", "GridGain", "jdbc"] },
  { ...JDBC_BRIDGE, driverId: "iris", title: "InterSystems IRIS", titleLocalized: { zh: "InterSystems IRIS" }, keywords: ["IRIS", "InterSystems", "jdbc"] },
  // jdbc-bridge: dedicated drivers whose vendor jar the user registers first
  {
    ...JDBC_BRIDGE,
    driverId: "cache",
    title: "InterSystems Caché",
    titleLocalized: { zh: "InterSystems Caché" },
    keywords: ["Caché", "Cache", "InterSystems Caché", "InterSystems Cache", "CacheDB", "jdbc"],
    requiresDriverJar: true,
  },
  {
    ...JDBC_BRIDGE,
    driverId: "sundb",
    title: "SUNDB",
    titleLocalized: { zh: "科蓝 SUNDB" },
    keywords: ["SUNDB", "科蓝", "Sunline", "jdbc"],
    requiresDriverJar: true,
  },
  {
    ...JDBC_BRIDGE,
    driverId: "oscar",
    title: "ShenTong OSCAR",
    titleLocalized: { zh: "神通 OSCAR" },
    keywords: ["神通", "神通数据库", "OSCAR", "ShenTong", "神舟通用", "jdbc"],
    requiresDriverJar: true,
  },
  { ...JDBC_BRIDGE, driverId: "generic", title: "JDBC Connection", titleLocalized: { zh: "JDBC 连接" }, keywords: ["JDBC", "Java", "通用 JDBC", "jdbc-bridge"] },
  // Official driver plugins
  { pluginId: "clickhouse-driver", pluginName: "ClickHouse 驱动 / ClickHouse Driver", driverId: "clickhouse", title: "ClickHouse", titleLocalized: { zh: "ClickHouse" }, replaces: "clickhouse", keywords: ["ClickHouse", "CH"] },
  { pluginId: "cloud-data-driver", pluginName: "云数据库驱动 / Cloud Data Driver", driverId: "dynamodb", title: "Amazon DynamoDB", titleLocalized: { zh: "Amazon DynamoDB" }, keywords: ["DynamoDB", "AWS", "Amazon"] },
  { pluginId: "cloud-data-driver", pluginName: "云数据库驱动 / Cloud Data Driver", driverId: "spanner", title: "Google Cloud Spanner", titleLocalized: { zh: "Google Cloud Spanner" }, keywords: ["Spanner", "GCP", "Google Cloud"] },
  { pluginId: "cloud-data-driver", pluginName: "云数据库驱动 / Cloud Data Driver", driverId: "salesforce", title: "Salesforce (SOQL)", titleLocalized: { zh: "Salesforce（SOQL）" }, keywords: ["Salesforce", "SOQL", "SFDC"] },
  { pluginId: "db2-driver", pluginName: "IBM Db2 驱动 / IBM Db2 Driver", driverId: "db2", title: "IBM Db2", titleLocalized: { zh: "IBM Db2" }, keywords: ["Db2", "DB2", "IBM"] },
  { pluginId: "duckdb-driver", pluginName: "DuckDB 驱动 / DuckDB Driver", driverId: "duckdb", title: "DuckDB", titleLocalized: { zh: "DuckDB" }, replaces: "duckdb", keywords: ["DuckDB"] },
  { pluginId: "edge-sqlite-driver", pluginName: "轻量 SQLite 驱动 / Edge SQLite Driver", driverId: "turso", title: "Turso / libSQL", titleLocalized: { zh: "Turso / libSQL" }, keywords: ["Turso", "libSQL", "SQLite"] },
  { pluginId: "edge-sqlite-driver", pluginName: "轻量 SQLite 驱动 / Edge SQLite Driver", driverId: "cloudflare-d1", title: "Cloudflare D1", titleLocalized: { zh: "Cloudflare D1" }, keywords: ["Cloudflare", "D1", "SQLite"] },
  { pluginId: "edge-sqlite-driver", pluginName: "轻量 SQLite 驱动 / Edge SQLite Driver", driverId: "rqlite", title: "rqlite", titleLocalized: { zh: "rqlite" }, keywords: ["rqlite", "SQLite"] },
  { pluginId: "enterprise-driver", pluginName: "企业数据库驱动 / Enterprise Database Driver", driverId: "saphana", title: "SAP HANA", titleLocalized: { zh: "SAP HANA" }, keywords: ["SAP", "HANA"] },
  { pluginId: "enterprise-driver", pluginName: "企业数据库驱动 / Enterprise Database Driver", driverId: "vertica", title: "Vertica", titleLocalized: { zh: "Vertica" }, keywords: ["Vertica"] },
  { pluginId: "enterprise-driver", pluginName: "企业数据库驱动 / Enterprise Database Driver", driverId: "exasol", title: "Exasol", titleLocalized: { zh: "Exasol" }, keywords: ["Exasol"] },
  { pluginId: "hive-driver", pluginName: "Hadoop SQL 驱动 / Hadoop SQL Driver", driverId: "hive", title: "Apache Hive", titleLocalized: { zh: "Apache Hive" }, keywords: ["Hive", "Hadoop", "HiveServer2"] },
  { pluginId: "hive-driver", pluginName: "Hadoop SQL 驱动 / Hadoop SQL Driver", driverId: "impala", title: "Apache Impala", titleLocalized: { zh: "Apache Impala" }, keywords: ["Impala", "Hadoop"] },
  { pluginId: "hive-driver", pluginName: "Hadoop SQL 驱动 / Hadoop SQL Driver", driverId: "sparksql", title: "Spark SQL (Thrift Server)", titleLocalized: { zh: "Spark SQL（Thrift Server）" }, keywords: ["Spark", "Spark SQL", "Thrift"] },
  { pluginId: "hive-driver", pluginName: "Hadoop SQL 驱动 / Hadoop SQL Driver", driverId: "kyuubi", title: "Apache Kyuubi", titleLocalized: { zh: "Apache Kyuubi" }, keywords: ["Kyuubi"] },
  { pluginId: "hive-driver", pluginName: "Hadoop SQL 驱动 / Hadoop SQL Driver", driverId: "inceptor", title: "Transwarp Inceptor", titleLocalized: { zh: "星环 Inceptor" }, keywords: ["Inceptor", "Transwarp", "星环", "TDH"] },
  { pluginId: "hive-driver", pluginName: "Hadoop SQL 驱动 / Hadoop SQL Driver", driverId: "argodb", title: "Transwarp ArgoDB", titleLocalized: { zh: "星环 ArgoDB" }, keywords: ["ArgoDB", "Transwarp", "星环"] },
  { pluginId: "nosql-driver", pluginName: "NoSQL 数据库驱动 / NoSQL Driver", driverId: "hbase", title: "Apache HBase", titleLocalized: { zh: "Apache HBase" }, keywords: ["HBase", "Hadoop"] },
  { pluginId: "nosql-driver", pluginName: "NoSQL 数据库驱动 / NoSQL Driver", driverId: "nebula", title: "NebulaGraph", titleLocalized: { zh: "NebulaGraph 图数据库" }, keywords: ["Nebula", "NebulaGraph", "nGQL", "图数据库"] },
  { pluginId: "oracle-driver", pluginName: "Oracle 驱动 / Oracle Driver", driverId: "oracle", title: "Oracle", titleLocalized: { zh: "Oracle" }, replaces: "oracle", keywords: ["Oracle"] },
  { pluginId: "pgcompat-driver", pluginName: "国产 PostgreSQL 兼容驱动 / PG-Compatible Driver", driverId: "uxdb", title: "UXDB", titleLocalized: { zh: "优炫数据库 UXDB" }, keywords: ["UXDB", "优炫", "PostgreSQL"] },
  { pluginId: "pgcompat-driver", pluginName: "国产 PostgreSQL 兼容驱动 / PG-Compatible Driver", driverId: "kwdb", title: "KaiwuDB", titleLocalized: { zh: "开务数据库 KaiwuDB" }, keywords: ["KaiwuDB", "KWDB", "开务", "PostgreSQL"] },
  { pluginId: "phoenix-driver", pluginName: "Apache Phoenix 驱动 / Apache Phoenix Driver", driverId: "phoenix", title: "Apache Phoenix", titleLocalized: { zh: "Apache Phoenix" }, keywords: ["Phoenix", "HBase"] },
  { pluginId: "search-driver", pluginName: "搜索引擎驱动 / Search Engine Driver", driverId: "solr", title: "Apache Solr", titleLocalized: { zh: "Apache Solr" }, keywords: ["Solr", "Lucene", "搜索"] },
  { pluginId: "search-driver", pluginName: "搜索引擎驱动 / Search Engine Driver", driverId: "manticore", title: "Manticore Search", titleLocalized: { zh: "Manticore Search" }, keywords: ["Manticore", "Sphinx", "搜索"] },
  { pluginId: "search-driver", pluginName: "搜索引擎驱动 / Search Engine Driver", driverId: "easysearch", title: "INFINI Easysearch", titleLocalized: { zh: "INFINI Easysearch" }, keywords: ["Easysearch", "INFINI", "极限科技", "搜索"] },
  { pluginId: "timeseries-driver", pluginName: "时序数据库驱动 / Time-Series Driver", driverId: "iotdb", title: "Apache IoTDB", titleLocalized: { zh: "Apache IoTDB" }, keywords: ["IoTDB", "时序"] },
  { pluginId: "timeseries-driver", pluginName: "时序数据库驱动 / Time-Series Driver", driverId: "influxdb3", title: "InfluxDB 3", titleLocalized: { zh: "InfluxDB 3" }, keywords: ["InfluxDB", "Influx", "时序"] },
  { pluginId: "timeseries-driver", pluginName: "时序数据库驱动 / Time-Series Driver", driverId: "victoriametrics", title: "VictoriaMetrics", titleLocalized: { zh: "VictoriaMetrics" }, keywords: ["VictoriaMetrics", "PromQL", "MetricsQL", "时序"] },
  { pluginId: "xugudb-driver", pluginName: "虚谷数据库驱动 / XuguDB Driver", driverId: "xugu", title: "XuguDB", titleLocalized: { zh: "虚谷数据库" }, keywords: ["XuguDB", "Xugu", "虚谷"] },
  { pluginId: "yashandb-driver", pluginName: "崖山数据库驱动 / YashanDB Driver", driverId: "yashandb", title: "YashanDB", titleLocalized: { zh: "崖山 YashanDB" }, replaces: "yashandb", keywords: ["YashanDB", "崖山"] },
];

/** Stable key of a suggestion: `<plugin>/<driver>`. */
export function pluginDriverSuggestionKey(suggestion: Pick<PluginDriverSuggestion, "pluginId" | "driverId">): string {
  return `${suggestion.pluginId}/${suggestion.driverId}`;
}

/**
 * Search keywords of an official plugin driver (its suggestion), so
 * an installed driver card is found by the same terms, e.g. 虚谷 for XuguDB
 * in the English UI. Empty for unknown drivers.
 */
export function pluginDriverSuggestionKeywords(pluginId: string, driverId: string): readonly string[] {
  const suggestion = PLUGIN_DRIVER_SUGGESTIONS.find(
    (item) => item.pluginId === pluginId && item.driverId === driverId,
  );
  return suggestion ? [...Object.values(suggestion.titleLocalized ?? {}), ...suggestion.keywords] : [];
}

/** Search normalization of the type picker, also folding accents (Caché matches cache). */
function normalizeSuggestionSearch(value: string): string {
  return normalizeDatabaseTypeSearch(value.normalize("NFKD").replace(/\p{M}/gu, ""));
}

const searchTexts = new Map(
  PLUGIN_DRIVER_SUGGESTIONS.map((suggestion) => [
    suggestion,
    [
      suggestion.title,
      ...Object.values(suggestion.titleLocalized ?? {}),
      suggestion.driverId,
      suggestion.pluginId,
      suggestion.pluginName,
      ...suggestion.keywords,
    ].map(normalizeSuggestionSearch),
  ]),
);

/**
 * Suggestions whose title, plugin or keywords match `query`. A driver whose
 * plugin is installed is skipped (the picker already lists it).
 */
export function searchPluginDriverSuggestions(
  query: string,
  isInstalled: (pluginId: string, driverId: string) => boolean,
): PluginDriverSuggestionMatch[] {
  const needle = normalizeSuggestionSearch(query);
  if (!needle) return [];
  const matches: PluginDriverSuggestionMatch[] = [];
  for (const suggestion of PLUGIN_DRIVER_SUGGESTIONS) {
    if (!searchTexts.get(suggestion)?.some((text) => text.includes(needle))) continue;
    if (isInstalled(suggestion.pluginId, suggestion.driverId)) continue;
    matches.push({ suggestion });
  }
  return matches;
}
