// Run from the repository root: swift tools/plugins/generate-covers.swift [plugin-id ...]
// Original, local artwork; no remote assets or screenshot claims.
// Every cover shares the MinCode accent background; the illustration (picked per plugin, else by marketplace
// category) and the plugin's own accent on its highlights tell listings apart. Output is a 1920×960 JPEG at
// official-plugins/<id>/assets/cover.jpg (plugin.json is pointed at it); COVER_OUT=<dir> writes previews to <dir>/<id>/cover.jpg.
import AppKit

let plugins: [(String, String, String, String, String, [String])] = [
 ("etcd-manager", "etcd", "KEYS / REVISIONS / MEMBERS", "KV", "60A5FA", ["DIRECTORY", "KEY / VALUE", "CLUSTER"]),
 ("grpc-debug", "gRPC", "REQUEST / RESPONSE", "RPC", "38BDF8", ["SERVICE", "METHOD", "RESPONSE"]),
 ("graphql-debugger", "GraphQL", "QUERY / MUTATION / SCHEMA", "GQL", "E10098", ["QUERY", "VARIABLES", "SCHEMA"]),
 ("kafka-inspector", "Kafka", "TOPICS / PARTITIONS / MESSAGES", "K", "FB923C", ["PRODUCER", "TOPIC", "CONSUMER"]),
 ("knowledge-base", "Knowledge", "CAPTURE / ORGANIZE / DISCOVER", "KB", "A78BFA", ["NOTES", "LIBRARY", "SEARCH"]),
 ("network-diagnostics", "Network", "CONNECTIVITY / LATENCY / ROUTES", "NET", "22D3EE", ["HOST", "ROUTE", "REACHABLE"]),
 ("openapi-contract", "OpenAPI", "SCHEMA / CONTRACT / VALIDATION", "API", "F472B6", ["SPEC", "SCHEMA", "VALIDATE"]),
 ("hbuilder-simulator", "uni-app & Mini Program", "BUILD / DEVICE / MINIPROGRAM", "UM", "FACC15", ["PROJECT", "DEVICE", "MP-WEIXIN"]),
 ("hosts-switcher", "Hosts Switcher", "SCHEMES / REMOTE / BACKUP", "#", "16A34A", ["SWITCH", "PREVIEW", "FLUSH DNS"]),
 ("arthas-console", "Arthas Console", "JVM DIAGNOSTICS / HTTP API", ">_", "F97316", ["DASHBOARD", "WATCH / TRACE", "JAD"]),
 ("http-load-tester", "HTTP Load", "CONCURRENCY / LATENCY / THROUGHPUT", "RPS", "F87171", ["REQUEST", "WORKERS", "PERCENTILES"]),
 ("mock-server", "Mock Server", "RULES / PROXY / CAPTURE", "MCK", "4ADE80", ["RULES", "UPSTREAM", "TRAFFIC"]),
 ("pprof-viewer", "pprof", "TOP / FLAME GRAPH / DIFF", "PP", "38BDF8", ["PROFILE", "FLAME", "DIFF"]),
 ("leetcode-cn", "LeetCode CN", "PROBLEMS / RUN / SUBMIT", "LC", "FFA116", ["PROBLEM", "CODE", "ACCEPTED"]),
 ("docker-manager", "Docker", "CONTAINERS / IMAGES / LOGS", "DK", "60A5FA", ["CONTAINER", "IMAGE", "VOLUME"]),
 ("nats-client", "NATS", "PUB-SUB / JETSTREAM / KV", "N", "34D399", ["SUBJECT", "STREAM", "BUCKET"]),
 ("rabbitmq-inspector", "RabbitMQ", "EXCHANGES / QUEUES / MESSAGES", "MQ", "FB923C", ["EXCHANGE", "BINDING", "QUEUE"]),
 ("pulsar-inspector", "Pulsar", "TENANTS / TOPICS / SUBSCRIPTIONS", "P", "818CF8", ["TENANT", "TOPIC", "SUBSCRIPTION"]),
 ("nacos-manager", "Nacos", "CONFIGS / HISTORY / SERVICES", "NC", "2DD4BF", ["NAMESPACE", "CONFIG", "INSTANCE"]),
 ("consul-manager", "Consul", "KV / SERVICES / HEALTH", "CS", "F472B6", ["KEY / VALUE", "SERVICE", "HEALTH"]),
 ("zookeeper-manager", "ZooKeeper", "ZNODES / STAT / ACL", "ZK", "FACC15", ["PATH", "DATA", "VERSION"]),
 ("object-storage", "Object Storage", "BUCKETS / OBJECTS / PRESIGN", "S3", "FDBA74", ["BUCKET", "OBJECT", "SHARE"]),
 ("prometheus-query", "Prometheus", "PROMQL / GRAPH / TARGETS", "PQ", "F97316", ["QUERY", "SERIES", "TARGETS"]),
 ("rocketmq-inspector", "RocketMQ", "TOPICS / GROUPS / MESSAGES", "RMQ", "EF4444", ["TOPIC", "GROUP", "OFFSET"]),
 ("log-explorer", "Log Explorer", "LOKI / SLS / ELASTICSEARCH", "LOG", "A3E635", ["QUERY", "STREAM", "CONTEXT"]),
 ("trace-viewer", "Trace Viewer", "JAEGER / ZIPKIN / SKYWALKING", "TR", "22D3EE", ["TRACE", "SPAN", "SERVICE"]),
 ("ci-pipelines", "CI Pipelines", "JENKINS / GITLAB CI / ACTIONS", "CI", "C084FC", ["PIPELINE", "JOB", "LOG"]),
 ("code-review", "Code Review", "GITEE / GITLAB / GITHUB", "PR", "FB7185", ["PULL REQUEST", "DIFF", "COMMENT"]),
 ("apollo-manager", "Apollo", "APPS / NAMESPACES / RELEASES", "AP", "818CF8", ["APP", "NAMESPACE", "RELEASE"]),
 ("spring-boot-actuator", "Spring Boot", "HEALTH / METRICS / LOGGERS", "SB", "6DB33F", ["HEALTH", "METRICS", "THREADS"]),
 ("jfr-viewer", "JFR", "CPU / ALLOCATION / FLAME GRAPH", "JFR", "F59E0B", ["RECORDING", "FLAME", "HOT METHODS"]),
 ("mail-catcher", "Mail Catcher", "SMTP / PREVIEW / ATTACHMENTS", "@", "38BDF8", ["INBOX", "HTML", "HEADERS"]),
 ("mcp-inspector", "MCP Inspector", "TOOLS / RESOURCES / PROMPTS", "MCP", "A78BFA", ["SERVER", "TOOL CALL", "JSON-RPC"]),
 ("xxl-job-manager", "XXL-JOB", "JOBS / TRIGGERS / LOGS", "JOB", "F43F5E", ["EXECUTOR", "SCHEDULE", "LOG"]),
 ("work-report", "Work Report", "COMMITS / DAILY / WEEKLY", "WR", "14B8A6", ["GIT LOG", "SUMMARY", "REPORT"]),
 ("wechat-formatter", "WeChat Article", "MARKDOWN / THEMES / COPY", "MD", "22C55E", ["WRITE", "STYLE", "PUBLISH"]),
 ("market-watch", "Market Watch", "STOCKS / FUNDS / INDICES", "¥", "EAB308", ["WATCHLIST", "QUOTES", "PROFIT"]),
 ("team-bot", "Team Bot", "FEISHU / DINGTALK / WECOM", "BOT", "3B82F6", ["WEBHOOK", "STREAMING", "AI REPLY"]),
 ("ai-config-hub", "AI Config Hub", "MCP / SKILLS / RULES", "AI", "8B5CF6", ["CLAUDE CODE", "CODEX", "CURSOR"]),
 ("cc-switch-sync", "CC Switch Sync", "PROVIDERS / MCP / SKILLS", "CC", "F97316", ["INCREMENTAL", "REVIEW", "ONE CLICK"]),
 ("ai-market", "Connector & Skill Market", "MCP CONNECTORS / AGENT SKILLS", "MKT", "14B8A6", ["SEARCH", "INSTALL", "UPGRADE"]),
 ("wsl", "WSL", "DISTROS / CONTAINERS / WSLC", "WSL", "0EA5E9", ["DISTRO", "CONTAINER", "RESOURCES"]),
 ("rdp-client", "Remote Desktop", "RDP / NLA / MULTI-SESSION", "RDP", "38BDF8", ["CONNECT", "DESKTOP", "CLIPBOARD"]),
 ("keymaps", "Keymaps", "PRESETS / CHORDS / MODAL EDITING", "⌘K", "3B82F6", ["WORKBENCH", "EDITOR", "MODAL"]),
 ("themes", "Themes", "WORKBENCH / EDITOR / TERMINAL", "◐", "B794F6", ["NIGHTFALL", "FOREST", "SEPIA"]),
 ("image-toolkit", "Image Toolkit", "COMPRESS / RESIZE / CONVERT", "IMG", "10B981", ["BATCH", "RESIZE", "PRIVACY"]),
 ("pdf-toolkit", "PDF Toolkit", "MERGE / SPLIT / COMPRESS", "PDF", "EF4444", ["MERGE", "PAGES", "WATERMARK"]),
 ("focus-timer", "Focus Timer", "POMODORO / BREAKS / STATS", "25", "F97316", ["FOCUS", "BREAK", "STATS"]),
 ("desktop-pet", "Desktop Pet", "COMPANIONS / MOODS / REMINDERS", "••", "F59E7A", ["COMPANION", "PERSONALITY", "REMINDERS"]),
 ("mini-games", "Mini Games", "SNAKE / 2048 / MINES / TETRIS", "▶", "22C55E", ["SNAKE", "2048", "TETRIS"]),
 ("port-manager", "Port Manager", "LISTENING PORTS / PROCESSES / KILL", ":", "38BDF8", ["PORT", "PROCESS", "KILL"]),
 ("feed-reader", "Feed Reader", "RSS / TRENDING / DIGEST", "RSS", "F59E0B", ["FEEDS", "TRENDING", "AI DIGEST"]),
 ("mind-map", "Mind Map", "OUTLINE / BRANCHES / EXPORT", "MAP", "8B5CF6", ["NODES", "LAYOUT", "EXPORT"]),
 ("ai-translator", "AI Translator", "TEXT / DOCUMENTS / GLOSSARY", "译", "06B6D4", ["TRANSLATE", "GLOSSARY", "BILINGUAL"]),
 ("clipboard-history", "Clipboard", "HISTORY / PINS / SEARCH", "CLIP", "64748B", ["HISTORY", "PINNED", "SEARCH"]),
 ("terminal-colors", "Terminal Colors", "DRACULA / NORD / SOLARIZED", ">_", "22D3EE", ["DARK", "LIGHT", "16 ANSI"]),
 ("file-icons", "File Icons", "COLOR / MONO / FOLDERS", "</>", "F59E0B", ["FILES", "FOLDERS", "LIGHT / DARK"]),
 ("editor-themes", "Editor Themes", "DRACULA / ONE DARK / GITHUB", "{ }", "A78BFA", ["DARK", "LIGHT", "SYNTAX"]),
 ("excalidraw", "Excalidraw", "SKETCH / DIAGRAM / SVG", "✎", "F59E0B", ["WHITEBOARD", "DIAGRAM", "SVG"]),
 ("draw", "Draw", "FLOWCHART / ARCHITECTURE / ER", "◇", "F97316", ["DRAW.IO", "OFFLINE", "MATH"]),
 ("dev-shells", "Dev Shells", "VENV / CONDA / WSL", ">_", "0EA5E9", ["VENV", "CONDA", "WSL"]),
 ("external-linters", "External Linters", "RUFF / SHELLCHECK / HADOLINT", "LNT", "F59E0B", ["RUFF", "GOLANGCI", "HADOLINT"]),
 ("dev-tasks", "Dev Tasks", "NPM / MAKE / GRADLE", "▶", "22C55E", ["DETECT", "RUN", "CONFIRM"]),
 ("python-debug", "Python Debugger", "BREAKPOINTS / STEP / VARIABLES", "PY", "FACC15", ["LAUNCH", "ATTACH", "DEBUGPY"]),
 ("java-debug", "Java Debugger", "BREAKPOINTS / STEP / VARIABLES", "JV", "F97316", ["LAUNCH", "ATTACH", "JDWP"]),
 ("extra-language-servers", "Language Servers", "CLOJURE / XML / VERILOG", "LSP", "A855F7", ["CLOJURE", "XML", "RST"]),
 ("code-format", "Code Format", "SHELL / TERRAFORM / XML", "FMT", "6366F1", ["FORMAT", "LINT", "DIAGNOSTICS"]),
 ("database-seed-generator", "Seed Generator", "DDL / TEST DATA / INSERT SQL", "SQL", "34D399", ["SCHEMA", "RULES", "INSERT"]),
 ("code-snippets", "Code Snippets", "GO / TS / VUE / PYTHON / SQL", "$0", "34D399", ["PREFIX", "TABSTOPS", "12 LANGUAGES"]),
 ("file-templates", "File Templates", "GITIGNORE / LICENSE / DOCKER", "+F", "60A5FA", ["FILES", "PROJECTS", "VARIABLES"]),
 ("http-codegen-pack", "HTTP Codegen", "HTTPX / OKHTTP / URLSESSION", "API", "0EA5E9", ["10 LANGUAGES", "ESCAPED", "REDACTED"]),
 ("terminal-links", "Terminal Links", "TRACEBACKS / STACKS / COMPILERS", ":12", "14B8A6", ["PYTHON", "GO", "NODE"]),
 ("ai-prompts", "AI Prompts", "REVIEW / TESTS / COMMITS", "/", "EC4899", ["PROMPTS", "SKILLS", "BILINGUAL"]),
 ("lang-zh-hant", "繁體中文", "TRADITIONAL CHINESE / TW / HK", "繁", "F43F5E", ["ZH-HANT-TW", "ZH-HANT-HK", "FALLBACK"]),
 ("getting-started", "Getting Started", "WORKSPACES / AI / PLUGINS", "?", "10B981", ["10 STEPS", "BILINGUAL", "ON DEMAND"]),
 ("lang-ja", "日本語", "JAPANESE / JA", "あ", "FB7185", ["JA", "HOST UI", "FALLBACK"]),
 ("lang-ko", "한국어", "KOREAN / KO", "한", "60A5FA", ["KO", "HOST UI", "FALLBACK"]),
 ("lang-de", "Deutsch", "GERMAN / DE", "De", "FACC15", ["DE", "HOST UI", "FALLBACK"]),
 ("lang-fr", "Français", "FRENCH / FR", "Fr", "818CF8", ["FR", "HOST UI", "FALLBACK"]),
 ("lang-es", "Español", "SPANISH / ES", "Es", "FB923C", ["ES", "HOST UI", "FALLBACK"]),
 ("lang-pt-br", "Português (Brasil)", "PORTUGUESE / BRAZIL / PT-BR", "Pt", "34D399", ["PT-BR", "HOST UI", "FALLBACK"]),
 ("lang-ru", "Русский", "RUSSIAN / RU", "Ру", "38BDF8", ["RU", "HOST UI", "FALLBACK"]),
 ("lang-it", "Italiano", "ITALIAN / IT", "It", "4ADE80", ["IT", "HOST UI", "FALLBACK"]),
 ("lang-vi", "Tiếng Việt", "VIETNAMESE / VI", "Vi", "F87171", ["VI", "HOST UI", "FALLBACK"]),
 ("lang-tr", "Türkçe", "TURKISH / TR", "Tr", "E879F9", ["TR", "HOST UI", "FALLBACK"]),
 ("invoice-manager", "Invoice Manager", "OFD / PDF / XML E-INVOICES", "票", "F59E0B", ["IMPORT", "DEDUPE", "REIMBURSE"]),
 ("ocr", "OCR", "IMAGES / SCANNED PDF / OFFLINE", "文", "8B5CF6", ["DETECT", "RECOGNIZE", "COPY TEXT"]),
 ("calendar", "Calendar", "MONTH / WEEK / AGENDA", "31", "3B82F6", ["EVENTS", "REMINDERS", "ICS SYNC"]),
 ("doc-converter", "Doc Converter", "MARKDOWN / WORD / PDF / HTML", "DOC", "2563EB", ["TEMPLATES", "PREVIEW", "BATCH"]),
 ("dev-almanac", "Dev Almanac", "DO / DON'T / LUCKY HOURS", "宜", "DC2626", ["DAILY", "FORTUNE", "SHARE"]),
 ("off-work-countdown", "Off-Work Countdown", "CLOCK OUT / WEEKEND / PAYDAY", "18:00", "10B981", ["COUNTDOWN", "STATUS BAR", "HOLIDAYS"]),
 ("lucky-wheel", "Lucky Wheel", "WHAT TO EAT / PICK / GROUP", "吃", "F97316", ["SPIN", "ROLL CALL", "TEAMS"]),
 ("batch-rename", "Batch Rename", "RULES / PREVIEW / UNDO", "A→B", "6366F1", ["RULES", "PREVIEW", "UNDO"]),
 ("issue-tracker", "Issue Tracker", "JIRA / GITHUB / GITLAB / GITEE", "#", "0EA5E9", ["MY ISSUES", "TRANSITION", "BRANCH"]),
 ("lan-transfer", "LAN Transfer", "QR CODE / PHONE / NO CLOUD", "⇅", "22C55E", ["SCAN", "SEND FILES", "TEXT"]),
 ("ecommerce-watch", "Cross-border Watch", "PRICE / STOCK / SHOPIFY / ALERTS", "$↘", "F97316", ["PRICE TREND", "STOCK ALERTS", "STORE CATALOG"]),
 ("crossborder-toolbox", "Cross-border Toolbox", "FX / SIZES / BARCODES / LISTINGS / SEO", "跨", "14B8A6", ["CONVERT & LOOK UP", "IMAGE CHECKS", "AI LISTINGS"]),
 ("test-runners", "Test Runners", "GO / VITEST / JEST / PYTEST / PHPUNIT", "TST", "22C55E", ["DISCOVER", "RUN", "RESULTS"]),
 ("todo-tree", "TODO Tree", "TODO / FIXME / HACK", "TDO", "F59E0B", ["BY FILE", "BY TAG", "JUMP TO LINE"]),
 ("api-routes", "API Routes", "GIN / SPRING / EXPRESS / FASTAPI / LARAVEL", "API", "8B5CF6", ["19 FRAMEWORKS", "PREFIXES", "JUMP TO LINE"]),
 ("i18n-helper", "i18n Helper", "VUE-I18N / I18NEXT / JSON / YAML", "I18", "2F80ED", ["HOVER", "MISSING", "KEY COMPLETION"]),
 ("laravel-helper", "Laravel Helper", "ROUTES / CONFIG / VIEWS / BLADE", "LV", "FF2D20", ["COMPLETION", "HOVER", "GO TO DEFINITION"]),
 ("config-hints", "Config Hints", "DOCKERFILE / NGINX / .ENV / CRON", "CFG", "0EA5E9", ["HOVER", "COMPLETION", "CRON IN WORDS"]),
 ("site-downloader", "Site Downloader", "CRAWL / RENDER / OFFLINE COPY", "WWW", "06B6D4", ["DEPTH & SCOPE", "JS RENDERING", "RESUME"]),
 ("schema-studio", "Schema Studio", "ENTITIES / MAPPERS / DATA DICTIONARY", "DDL", "0EA5E9", ["TABLES", "CODEGEN", "DICTIONARY"]),
 ("php-env-manager", "Dev Environment", "PHP / NODE / PYTHON / GO / JAVA / DATABASES", "DEV", "10B981", ["83 PACKAGES", "VERSIONS / SERVICES", "SITES / CONFIGS"]),
 ("download-manager", "Download Manager", "HTTP / BT MAGNET / QUEUES / VIDEO", "↓", "3B82F6", ["MULTI-CONNECTION", "BT MAGNET", "QUEUES"]),
 ("crypto-toolkit", "Crypto Toolkit", "PHP ENCODE / AES / HASH / JWT / RSA", "KEY", "F59E0B", ["ENCRYPT", "HASH / HMAC", "RSA / JWT"]),
 ("certificate-manager", "Domains & Certs", "DNS / SSL / ACME", "TLS", "10B981", ["DNS RECORDS", "CERTIFICATES", "RENEWAL"]),
 ("devtools", "DevTools", "JSON / DIFF / REGEX / CRON", "{ }", "8B5CF6", ["CONVERT", "FORMAT", "GENERATE"]),
 ("todo", "Todo", "INBOX / TODAY / PLANNED", "✓", "22C55E", ["SUBTASKS", "REPEAT", "FOCUS"]),
 ("prd-studio", "PRD Studio", "TEMPLATES / FEATURES / AI REVIEW", "PRD", "6366F1", ["SECTIONS", "PRIORITIES", "REVISIONS"]),
 ("english-learning", "English Learning", "CET-4 / CET-6 / FLASHCARDS / SPELLING", "Aa", "22C55E", ["FLASHCARDS", "QUIZ", "REVIEW"]),
 ("uni-app-support", "uni-app", "UNI-APP / UNI-APP X / UTS / UVUE", "uni", "2B9939", ["SNIPPETS", "UTS HIGHLIGHT", "CONFIG HINTS"]),
 ("clickhouse-driver", "ClickHouse Driver", "NATIVE TCP / STREAMING / DDL", "CH", "FACC15", ["QUERY", "STREAM", "SCHEMA"]),
 ("duckdb-driver", "DuckDB Driver", "EMBEDDED / FILE / IN-MEMORY", "DDB", "FBBF24", ["QUERY", "EDIT ROWS", "DDL"]),
 ("oracle-driver", "Oracle Driver", "PURE GO / SERVICE NAME / SID", "ORA", "EF4444", ["QUERY", "METADATA", "DDL"]),
 ("yashandb-driver", "YashanDB Driver", "YASHANDB / 崖山 / NATIVE CLIENT", "YAS", "3B82F6", ["QUERY", "SCHEMA", "EDIT ROWS"]),
 ("pgcompat-driver", "PG-Compatible Driver", "UXDB / KAIWUDB / PGX / TLS", "PG", "60A5FA", ["QUERY", "READ-ONLY", "SCHEMA"]),
 ("edge-sqlite-driver", "Edge SQLite Driver", "TURSO / CLOUDFLARE D1 / RQLITE", "SQL", "38BDF8", ["HTTP SQL", "SCHEMA", "EDIT ROWS"]),
 ("cloud-data-driver", "Cloud Data Driver", "DYNAMODB / SPANNER / SALESFORCE", "☁", "818CF8", ["PARTIQL", "GOOGLESQL", "SOQL"]),
 ("enterprise-driver", "Enterprise Driver", "SAP HANA / VERTICA / EXASOL", "ENT", "6366F1", ["QUERY", "SCHEMA", "DDL"]),
 ("hive-driver", "Hadoop SQL Driver", "HIVE / IMPALA / SPARK / KYUUBI", "HQL", "F97316", ["HIVESERVER2", "SASL / TLS", "SCHEMA"]),
 ("nosql-driver", "NoSQL Driver", "HBASE / NEBULAGRAPH / NGQL", "NoS", "A855F7", ["SCAN / GET", "GRAPH", "NAMESPACES"]),
 ("search-driver", "Search Engine Driver", "SOLR / MANTICORE / EASYSEARCH", "⌕", "14B8A6", ["SQL", "LUCENE", "INDEXES"]),
 ("timeseries-driver", "Time-Series Driver", "IOTDB / INFLUXDB 3 / VICTORIAMETRICS", "TS", "10B981", ["SQL", "INFLUXQL", "METRICSQL"]),
 ("db2-driver", "IBM Db2 Driver", "DB2 LUW / PURE GO DRDA", "DB2", "0F62FE", ["QUERY", "SYSCAT", "EDIT ROWS"]),
 ("phoenix-driver", "Apache Phoenix Driver", "PHOENIX QUERY SERVER / HBASE", "PHX", "F97316", ["AVATICA", "SYSTEM.CATALOG", "PREVIEW"]),
 ("xugudb-driver", "XuguDB Driver", "XUGUDB / 虚谷 / PURE GO", "XG", "10B981", ["QUERY", "SCHEMA", "EDIT ROWS"]),
 ("ebook-reader", "Ebook Reader", "EPUB / MOBI / PDF / TXT", "EB", "6366F1", ["LIBRARY", "READER", "BOOKMARKS"]),
 ("file-viewer", "File Viewer", "OFFICE / PDF / IMAGES / CODE", "◉", "0EA5E9", ["PREVIEW", "FORMATS", "OFFLINE"]),
 ("kubernetes-manager", "Kubernetes", "PODS / DEPLOYMENTS / LOGS", "K8s", "326CE5", ["CLUSTER", "WORKLOAD", "LOGS"]),
 ("mqtt-client", "MQTT", "PUBLISH / SUBSCRIBE / MQTT 5", "MQ", "C026D3", ["BROKER", "TOPIC", "PAYLOAD"]),
 ("sheet-workbench", "Sheet Workbench", "CSV / EXCEL / JSON / SQL", "⊞", "16A34A", ["IMPORT", "QUERY", "EXPORT"]),
 ("web-navigation", "Web Navigation", "BOOKMARKS / GROUPS / SEARCH", "↗", "F59E0B", ["BOOKMARKS", "GROUPS", "SEARCH"]),
 ("cua-driver", "Cua Driver", "BACKGROUND APP CONTROL / MCP", "CUA", "8B5CF6", ["NO FOCUS STEAL", "UI TREE", "AI CONNECTOR"]),
 ("data-backup", "Data Backup", "S3 / SSH / WEBDAV / ENCRYPTED", "BAK", "0EA5E9", ["SCHEDULE", "ENCRYPT", "RESTORE"]),
 ("backup-target-folder", "Backup Folder", "LOCAL / SYNC DRIVE / ENCRYPTED", "DIR", "14B8A6", ["DATA BACKUP", "ATOMIC WRITE", "NO SYMLINKS"]),
 ("jdbc-bridge", "JDBC Bridge", "TERADATA / INFORMIX / ACCESS / H2 / IRIS", "JDBC", "E76F00", ["JAVA BRIDGE", "DRIVER JARS", "SHA-256"]),
 ("business-card", "Business Card", "PRINT PDF / VCARD / QR / BATCH", "名", "F59E0B", ["8 TEMPLATES", "DESIGN & BATCH", "AI DRAFTS"])
]
// The illustration: a per-plugin motif, else the marketplace category, else the plugin.json category.
let marketCategories: [String: String] = {
 guard let data = FileManager.default.contents(atPath: "vue-app/scripts/official-plugin-market-categories.json"),
       let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
 return json["categories"] as? [String: String] ?? [:]
}()
func category(_ slug: String) -> String? {
 guard let data = FileManager.default.contents(atPath: "official-plugins/\(slug)/plugin.json"),
       let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
 return motifs[slug] ?? marketCategories[slug] ?? json["category"] as? String ?? "other"
}

// Plugins whose job has a clearer picture than their category's default illustration.
let motifs: [String: String] = [
 "schema-studio": "spreadsheet", "zookeeper-manager": "tree", "etcd-manager": "tree", "search-driver": "search", "timeseries-driver": "chart",
 "code-review": "editor", "issue-tracker": "productivity", "file-viewer": "document", "object-storage": "cloud", "mail-catcher": "mail",
 "crypto-toolkit": "lock", "pprof-viewer": "flame", "jfr-viewer": "flame", "port-manager": "network", "hbuilder-simulator": "phone",
 "uni-app-support": "phone", "api-routes": "tree", "log-explorer": "development", "trace-viewer": "flame", "prometheus-query": "chart",
 "ci-pipelines": "messaging", "hosts-switcher": "network", "arthas-console": "development", "spring-boot-actuator": "chart",
 "keymaps": "keys", "todo-tree": "tree", "test-runners": "productivity", "file-templates": "document", "i18n-helper": "language", "laravel-helper": "editor",
 "terminal-links": "links", "calendar": "calendar", "dev-almanac": "calendar", "clipboard-history": "cards", "batch-rename": "cards",
 "download-manager": "download", "site-downloader": "download", "image-toolkit": "photo", "ocr": "document", "market-watch": "chart", "ecommerce-watch": "chart", "crossborder-toolbox": "cards",
 "english-learning": "book", "feed-reader": "network", "focus-timer": "clock", "off-work-countdown": "clock", "lan-transfer": "network",
 "team-bot": "messaging", "web-navigation": "web", "ebook-reader": "book", "knowledge-base": "book", "mind-map": "messaging",
 "work-report": "chart", "http-load-tester": "chart", "editor-themes": "editor",
 "cua-driver": "keys", "data-backup": "cloud", "backup-target-folder": "tree", "certificate-manager": "lock", "devtools": "development", "excalidraw": "sketch",
 "docker-manager": "containers", "wsl": "containers", "rdp-client": "remote", "draw": "flowchart", "prd-studio": "document", "leetcode-cn": "judge",
 "business-card": "businesscard",
]

func color(_ hex: String) -> NSColor {
 let n = UInt32(hex, radix: 16)!
 return NSColor(deviceRed: CGFloat((n >> 16) & 255)/255, green: CGFloat((n >> 8) & 255)/255, blue: CGFloat(n & 255)/255, alpha: 1)
}
// Deterministic per-plugin randomness, so reruns produce identical covers.
struct Seeded {
 var state: UInt64
 init(_ slug: String) { state = slug.unicodeScalars.reduce(UInt64(1469598103934665603)) { ($0 ^ UInt64($1.value)) &* 1099511628211 } }
 mutating func next() -> CGFloat {
  state = state &+ 0x9E3779B97F4A7C15
  var z = state
  z = (z ^ (z >> 30)) &* 0xBF58476D1CE4E5B9
  z = (z ^ (z >> 27)) &* 0x94D049BB133111EB
  return CGFloat((z ^ (z >> 31)) >> 11) / CGFloat(1 << 53)
 }
 mutating func range(_ a: CGFloat, _ b: CGFloat) -> CGFloat { a + (b - a) * next() }
}
let brandAccent = color("5D87FF").usingColorSpace(.deviceRGB)!
let white = NSColor.white
func w(_ alpha: CGFloat) -> NSColor { white.withAlphaComponent(alpha) }

func fill(_ path: NSBezierPath, _ c: NSColor) { c.setFill(); path.fill() }
func stroke(_ path: NSBezierPath, _ c: NSColor, _ width: CGFloat = 3) { c.setStroke(); path.lineWidth = width; path.lineCapStyle = .round; path.lineJoinStyle = .round; path.stroke() }
func rounded(_ x: CGFloat, _ y: CGFloat, _ w: CGFloat, _ h: CGFloat, _ r: CGFloat) -> NSBezierPath { NSBezierPath(roundedRect: NSRect(x: x, y: y, width: w, height: h), xRadius: r, yRadius: r) }
func oval(_ cx: CGFloat, _ cy: CGFloat, _ rx: CGFloat, _ ry: CGFloat) -> NSBezierPath { NSBezierPath(ovalIn: NSRect(x: cx - rx, y: cy - ry, width: rx * 2, height: ry * 2)) }
func line(_ points: [NSPoint]) -> NSBezierPath { let p = NSBezierPath(); p.move(to: points[0]); points.dropFirst().forEach { p.line(to: $0) }; return p }
func rotated(_ degrees: CGFloat, around center: NSPoint, _ draw: () -> Void) {
 NSGraphicsContext.saveGraphicsState()
 let t = NSAffineTransform(); t.translateX(by: center.x, yBy: center.y); t.rotate(byDegrees: degrees); t.translateX(by: -center.x, yBy: -center.y); t.concat()
 draw()
 NSGraphicsContext.restoreGraphicsState()
}
func polygon(_ cx: CGFloat, _ cy: CGFloat, _ r: CGFloat, sides: Int, rotation: CGFloat = 0) -> NSBezierPath {
 line((0...sides).map { i in let a = rotation + CGFloat(i) * 2 * .pi / CGFloat(sides); return NSPoint(x: cx + r * cos(a), y: cy + r * sin(a)) })
}
func sparkle(_ cx: CGFloat, _ cy: CGFloat, _ r: CGFloat) -> NSBezierPath {
 let p = NSBezierPath(); p.move(to: NSPoint(x: cx, y: cy + r))
 for (x, y) in [(cx, cy - r), (cx - r, cy), (cx + r, cy)] { _ = (x, y) }
 p.curve(to: NSPoint(x: cx + r, y: cy), controlPoint1: NSPoint(x: cx + r * 0.12, y: cy + r * 0.12), controlPoint2: NSPoint(x: cx + r * 0.12, y: cy + r * 0.12))
 p.curve(to: NSPoint(x: cx, y: cy - r), controlPoint1: NSPoint(x: cx + r * 0.12, y: cy - r * 0.12), controlPoint2: NSPoint(x: cx + r * 0.12, y: cy - r * 0.12))
 p.curve(to: NSPoint(x: cx - r, y: cy), controlPoint1: NSPoint(x: cx - r * 0.12, y: cy - r * 0.12), controlPoint2: NSPoint(x: cx - r * 0.12, y: cy - r * 0.12))
 p.curve(to: NSPoint(x: cx, y: cy + r), controlPoint1: NSPoint(x: cx - r * 0.12, y: cy + r * 0.12), controlPoint2: NSPoint(x: cx - r * 0.12, y: cy + r * 0.12))
 return p
}
func draw(_ s: String, at p: NSPoint, size: CGFloat, color c: NSColor, weight: NSFont.Weight = .bold, mono: Bool = false, centered: Bool = false) {
 let font = mono ? NSFont.monospacedSystemFont(ofSize: size, weight: weight) : NSFont.systemFont(ofSize: size, weight: weight)
 let attributes: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: c, .kern: mono ? 1.2 : 0]
 let measured = (s as NSString).size(withAttributes: attributes)
 (s as NSString).draw(at: centered ? NSPoint(x: p.x - measured.width / 2, y: p.y - measured.height / 2) : p, withAttributes: attributes)
}
func width(_ s: String, size: CGFloat, weight: NSFont.Weight = .bold, mono: Bool = false) -> CGFloat {
 let font = mono ? NSFont.monospacedSystemFont(ofSize: size, weight: weight) : NSFont.systemFont(ofSize: size, weight: weight)
 return (s as NSString).size(withAttributes: [.font: font, .kern: mono ? 1.2 : 0]).width
}

// The plugin mark on a solid tile in a dark shade of the plugin color, readable over white shapes too;
// language packs make the mark the hero instead.
var tileFill = NSColor.black
func markTile(_ mark: String, _ x: CGFloat, _ y: CGFloat, _ size: CGFloat) {
 let cx = min(max(x, 520 + size / 2), 960 - 28 - size / 2), cy = min(max(y, 24 + size / 2), 480 - 24 - size / 2)
 fill(rounded(cx - size / 2, cy - size / 2, size, size, size * 0.26), tileFill)
 stroke(rounded(cx - size / 2, cy - size / 2, size, size, size * 0.26), w(0.55), 2)
 draw(mark, at: NSPoint(x: cx, y: cy), size: mark.count > 2 ? size * 0.3 : size * 0.44, color: white, weight: .heavy, centered: true)
}

/// One illustration per marketplace category, centered on the right half; `hot` and `alt` are the plugin's own hues.
func illustration(_ kind: String, mark: String, hot: NSColor, alt: NSColor, rng: inout Seeded) {
 let cx: CGFloat = 728, cy: CGFloat = 248
 switch kind {
 case "database":
  for i in 0..<3 {
   let y = cy - 110 + CGFloat(i) * 78
   fill(rounded(cx - 120, y, 240, 70, 0), w(0.10 + CGFloat(i) * 0.05))
   fill(oval(cx, y, 120, 30), w(0.16 + CGFloat(i) * 0.05)); stroke(oval(cx, y, 120, 30), w(0.6), 2.5)
   stroke(line([NSPoint(x: cx - 120, y: y), NSPoint(x: cx - 120, y: y + 70)]), w(0.6), 2.5)
   stroke(line([NSPoint(x: cx + 120, y: y), NSPoint(x: cx + 120, y: y + 70)]), w(0.6), 2.5)
  }
  fill(oval(cx, cy + 124, 120, 30), w(0.28)); stroke(oval(cx, cy + 124, 120, 30), w(0.8), 2.5)
  markTile(mark, cx + 150, cy - 120, 104)
 case "messaging":
  let nodes = [NSPoint(x: cx - 170, y: cy + 90), NSPoint(x: cx - 170, y: cy - 90), NSPoint(x: cx, y: cy), NSPoint(x: cx + 170, y: cy + 110), NSPoint(x: cx + 170, y: cy), NSPoint(x: cx + 170, y: cy - 110)]
  for (a, b) in [(0, 2), (1, 2), (2, 3), (2, 4), (2, 5)] {
   let p = NSBezierPath(); p.move(to: nodes[a]); p.curve(to: nodes[b], controlPoint1: NSPoint(x: (nodes[a].x + nodes[b].x) / 2, y: nodes[a].y), controlPoint2: NSPoint(x: (nodes[a].x + nodes[b].x) / 2, y: nodes[b].y))
   stroke(p, w(0.35), 3)
   let t = rng.range(0.3, 0.7)
   fill(oval(nodes[a].x + (nodes[b].x - nodes[a].x) * t, nodes[a].y + (nodes[b].y - nodes[a].y) * t, 8, 8), white)
  }
  for (i, n) in nodes.enumerated() where i != 2 { fill(oval(n.x, n.y, 22, 22), w(0.22)); stroke(oval(n.x, n.y, 22, 22), w(0.7), 2.5) }
  markTile(mark, cx, cy, 112)
 case "network":
  let origin = NSPoint(x: cx - 150, y: cy - 130)
  for (i, r) in stride(from: CGFloat(90), through: 390, by: 75).enumerated() {
   let arc = NSBezierPath(); arc.appendArc(withCenter: origin, radius: r, startAngle: 0, endAngle: 90)
   stroke(arc, w(0.62 - CGFloat(i) * 0.1), 4)
  }
  fill(oval(origin.x, origin.y, 18, 18), white)
  for _ in 0..<5 { let a = rng.range(0.1, 1.45), r = rng.range(110, 360); fill(oval(origin.x + r * cos(a), origin.y + r * sin(a), 6, 6), w(0.85)) }
  markTile(mark, cx + 120, cy + 90, 108)
 case "devops":
  let r: CGFloat = 60, dx = r * sqrt(3)
  let centers = [NSPoint(x: cx, y: cy)] + (0..<6).map { i -> NSPoint in let a = CGFloat(i) * .pi / 3 + .pi / 6; return NSPoint(x: cx + dx * cos(a), y: cy + dx * sin(a)) }
  for (i, c) in centers.enumerated() {
   let hex = polygon(c.x, c.y, r - 6, sides: 6, rotation: .pi / 6)
   fill(hex, i == 0 ? w(0.0) : w(rng.range(0.08, 0.3))); stroke(hex, w(0.6), 2.5)
  }
  markTile(mark, cx, cy, 92)
 case "api":
  draw("{", at: NSPoint(x: cx - 175, y: cy + 8), size: 260, color: w(0.28), weight: .light, centered: true)
  draw("}", at: NSPoint(x: cx + 175, y: cy + 8), size: 260, color: w(0.28), weight: .light, centered: true)
  for (i, y) in [cy + 95, cy - 95].enumerated() {
   let from = i == 0 ? cx - 95 : cx + 95, to = i == 0 ? cx + 95 : cx - 95
   stroke(line([NSPoint(x: from, y: y), NSPoint(x: to, y: y)]), w(0.7), 4)
   let dir: CGFloat = to > from ? 1 : -1
   stroke(line([NSPoint(x: to - 16 * dir, y: y + 12), NSPoint(x: to, y: y), NSPoint(x: to - 16 * dir, y: y - 12)]), w(0.7), 4)
  }
  markTile(mark, cx, cy, 116)
 case "judge":
  // A code panel beside the judge's verdict: test cases ticking off, then "Accepted".
  let x = cx - 240, y = cy - 140
  fill(rounded(x, y, 220, 270, 18), NSColor.black.withAlphaComponent(0.28)); stroke(rounded(x, y, 220, 270, 18), w(0.35), 2)
  for row in 0..<6 {
   let ly = y + 220 - CGFloat(row) * 34, indent = CGFloat([0, 1, 2, 2, 1, 0][row]) * 18
   let first = rng.range(32, 60), second = rng.range(30, 72)
   fill(rounded(x + 24 + indent, ly - 4, first, 10, 5), row % 2 == 0 ? hot : alt)
   fill(rounded(x + 24 + indent + first + 10, ly - 4, second, 10, 5), w(0.55))
  }
  let vx = cx + 10, vw: CGFloat = 210
  for i in 0..<3 {
   let ry = cy + 70 - CGFloat(i) * 62
   fill(rounded(vx, ry, vw, 46, 12), w(0.16)); stroke(rounded(vx, ry, vw, 46, 12), w(0.4), 2)
   fill(oval(vx + 28, ry + 23, 13, 13), hot)
   stroke(line([NSPoint(x: vx + 21, y: ry + 23), NSPoint(x: vx + 26, y: ry + 17), NSPoint(x: vx + 35, y: ry + 29)]), white, 3)
   fill(rounded(vx + 54, ry + 18, rng.range(70, 120), 10, 5), w(0.6))
  }
  fill(rounded(vx, cy - 132, vw, 54, 14), hot)
  draw("ACCEPTED", at: NSPoint(x: vx + vw / 2, y: cy - 105), size: 22, color: white, weight: .heavy, centered: true)
  markTile(mark, x + 220, y + 270, 84)
 case "development", "editor":
  let x = cx - 190, y = cy - 140
  fill(rounded(x, y, 380, 270, 18), NSColor.black.withAlphaComponent(0.28)); stroke(rounded(x, y, 380, 270, 18), w(0.35), 2)
  for (i, c) in [NSColor(deviceRed: 1, green: 0.37, blue: 0.34, alpha: 1), NSColor(deviceRed: 1, green: 0.74, blue: 0.18, alpha: 1), NSColor(deviceRed: 0.16, green: 0.79, blue: 0.25, alpha: 1)].enumerated() {
   fill(oval(x + 26 + CGFloat(i) * 22, y + 246, 7, 7), c)
  }
  for row in 0..<6 {
   let ly = y + 200 - CGFloat(row) * 32, indent = CGFloat([0, 1, 2, 2, 1, 0][row]) * 26
   if kind == "editor" { draw("\(row + 1)", at: NSPoint(x: x + 20, y: ly - 9), size: 13, color: w(0.35), weight: .regular, mono: true) }
   let start = x + (kind == "editor" ? 52 : 26) + indent
   let first = rng.range(40, 90), second = rng.range(50, 150)
   fill(rounded(start, ly - 4, first, 10, 5), row % 2 == 0 ? hot : alt)
   fill(rounded(start + first + 10, ly - 4, second, 10, 5), w(0.55))
  }
  if kind == "development" { draw(">_", at: NSPoint(x: x + 26, y: y + 14), size: 22, color: white, weight: .bold, mono: true) }
  else { fill(rounded(x + 52 + 210, y + 200 - 5 * 32 - 10, 3, 24, 1), white) }
  markTile(mark, x + 380, y + 270, 100)
 case "ai":
  var points: [NSPoint] = []
  for i in 0..<8 { let a = CGFloat(i) * .pi / 4 + rng.range(-0.3, 0.3), r = rng.range(110, 175); points.append(NSPoint(x: cx + r * cos(a), y: cy + r * sin(a))) }
  for i in 0..<points.count { stroke(line([points[i], points[(i + 1) % points.count]]), w(0.3), 2); if i % 2 == 0 { stroke(line([points[i], NSPoint(x: cx, y: cy)]), w(0.18), 2) } }
  for (i, p) in points.enumerated() { if i % 3 == 0 { fill(sparkle(p.x, p.y, 26), white) } else { fill(oval(p.x, p.y, 7, 7), w(0.9)) } }
  markTile(mark, cx, cy, 110)
 case "document":
  for (i, angle) in [CGFloat(-9), 5, 0].enumerated() {
   rotated(angle, around: NSPoint(x: cx, y: cy)) {
    let x = cx - 120 + CGFloat(i) * 14, y = cy - 155 + CGFloat(i) * 6
    fill(rounded(x, y, 230, 300, 14), i == 2 ? w(0.92) : w(0.22 + CGFloat(i) * 0.12))
    if i == 2 {
     fill(rounded(x + 28, y + 238, 120, 16, 8), hot)
     for row in 0..<6 { fill(rounded(x + 28, y + 200 - CGFloat(row) * 28, rng.range(110, 175), 9, 4.5), hot.withAlphaComponent(0.28)) }
    }
   }
  }
  markTile(mark, cx + 150, cy - 125, 96)
 case "productivity":
  fill(rounded(cx - 170, cy - 150, 300, 300, 22), w(0.16)); stroke(rounded(cx - 170, cy - 150, 300, 300, 22), w(0.4), 2)
  for row in 0..<4 {
   let y = cy + 90 - CGFloat(row) * 70, done = row < 2
   fill(oval(cx - 125, y, 17, 17), done ? white : w(0.0)); stroke(oval(cx - 125, y, 17, 17), white, 3)
   if done { stroke(line([NSPoint(x: cx - 133, y: y), NSPoint(x: cx - 127, y: y - 7), NSPoint(x: cx - 116, y: y + 8)]), hot, 4) }
   fill(rounded(cx - 92, y - 6, rng.range(110, 190), 12, 6), done ? w(0.45) : w(0.85))
  }
  markTile(mark, cx + 140, cy + 120, 104)
 case "language":
  let bubble = rounded(cx - 175, cy - 120, 330, 270, 60)
  fill(bubble, w(0.16)); stroke(bubble, w(0.55), 3)
  let tail = line([NSPoint(x: cx - 100, y: cy - 118), NSPoint(x: cx - 140, y: cy - 175), NSPoint(x: cx - 50, y: cy - 118)])
  fill(tail, w(0.16)); stroke(tail, w(0.55), 3)
  draw(mark, at: NSPoint(x: cx - 10, y: cy + 15), size: mark.count > 1 ? 130 : 170, color: white, weight: .heavy, centered: true)
 case "theme":
  let pivot = NSPoint(x: cx - 40, y: cy - 150)
  for i in 0..<5 {
   rotated(CGFloat(i - 2) * 16, around: pivot) {
    let h = (hot.hueComponent + CGFloat(i) * 0.12).truncatingRemainder(dividingBy: 1)
    fill(rounded(pivot.x - 40, pivot.y, 80, 290, 16), NSColor(deviceHue: h, saturation: 0.65, brightness: 0.95, alpha: 1))
    stroke(rounded(pivot.x - 40, pivot.y, 80, 290, 16), w(0.6), 2)
   }
  }
  fill(oval(pivot.x, pivot.y + 26, 9, 9), white)
  markTile(mark, cx + 160, cy - 110, 96)
 case "entertainment":
  for i in 0..<10 {
   let x = rng.range(cx - 190, cx + 190), y = rng.range(cy - 160, cy + 160), s = rng.range(18, 42)
   let c = i % 2 == 0 ? hot : (i % 3 == 0 ? white : alt)
   switch i % 4 {
   case 0: fill(oval(x, y, s, s), c.withAlphaComponent(0.8))
   case 1: fill(polygon(x, y, s, sides: 3, rotation: rng.range(0, 6)), c.withAlphaComponent(0.85))
   case 2: rotated(rng.range(0, 45), around: NSPoint(x: x, y: y)) { fill(rounded(x - s, y - s, s * 2, s * 2, 8), c.withAlphaComponent(0.75)) }
   default: fill(sparkle(x, y, s), c)
   }
  }
  markTile(mark, cx, cy, 120)
 case "spreadsheet":
  let x = cx - 190, y = cy - 140
  for r in 0..<6 { for c in 0..<5 {
   let cell = rounded(x + CGFloat(c) * 76, y + CGFloat(5 - r) * 48, 70, 42, 6)
   fill(cell, r == 0 ? w(0.55) : (rng.next() > 0.8 ? hot : w(0.14)))
  } }
  markTile(mark, cx + 175, cy + 135, 96)
 case "web":
  let x = cx - 190, y = cy - 140
  fill(rounded(x, y, 380, 280, 18), w(0.16)); stroke(rounded(x, y, 380, 280, 18), w(0.5), 2.5)
  fill(rounded(x + 20, y + 236, 340, 26, 13), w(0.25))
  let globe = NSPoint(x: cx, y: cy - 20)
  stroke(oval(globe.x, globe.y, 90, 90), white, 4); stroke(oval(globe.x, globe.y, 40, 90), w(0.8), 3)
  stroke(line([NSPoint(x: globe.x - 90, y: globe.y), NSPoint(x: globe.x + 90, y: globe.y)]), w(0.8), 3)
  markTile(mark, x + 380, y + 280, 96)
 case "calendar":
  let x = cx - 180, y = cy - 150
  fill(rounded(x, y, 360, 300, 22), w(0.16)); stroke(rounded(x, y, 360, 300, 22), w(0.45), 2)
  fill(rounded(x, y + 240, 360, 60, 22), hot.withAlphaComponent(0.85))
  for ring in [x + 90, x + 270] { fill(rounded(ring - 6, y + 278, 12, 40, 6), white) }
  let today = Int(rng.range(0, 20))
  for i in 0..<21 {
   let col = CGFloat(i % 7), row = CGFloat(i / 7)
   let cell = rounded(x + 22 + col * 46, y + 168 - row * 68, 38, 50, 8)
   fill(cell, i == today ? white : w(0.14))
   if i == today { draw("\(i + 1)", at: NSPoint(x: x + 41 + col * 46, y: y + 193 - row * 68), size: 18, color: hot, weight: .heavy, centered: true) }
  }
  markTile(mark, x + 360, y + 300, 92)
 case "clock":
  fill(oval(cx - 20, cy, 165, 165), w(0.14)); stroke(oval(cx - 20, cy, 165, 165), w(0.75), 5)
  for i in 0..<12 { let a = CGFloat(i) * .pi / 6, r1: CGFloat = i % 3 == 0 ? 128 : 140
   stroke(line([NSPoint(x: cx - 20 + r1 * cos(a), y: cy + r1 * sin(a)), NSPoint(x: cx - 20 + 152 * cos(a), y: cy + 152 * sin(a))]), w(0.8), i % 3 == 0 ? 5 : 3) }
  let progress = NSBezierPath(); progress.appendArc(withCenter: NSPoint(x: cx - 20, y: cy), radius: 112, startAngle: 90, endAngle: 90 - rng.range(100, 260), clockwise: true)
  stroke(progress, hot, 12)
  stroke(line([NSPoint(x: cx - 20, y: cy), NSPoint(x: cx - 20, y: cy + 92)]), white, 7)
  stroke(line([NSPoint(x: cx - 20, y: cy), NSPoint(x: cx + 46, y: cy - 30)]), white, 7)
  fill(oval(cx - 20, cy, 10, 10), white)
  markTile(mark, cx + 175, cy - 125, 96)
 case "cards":
  for i in 0..<4 {
   rotated(CGFloat(3 - i) * 5 - 6, around: NSPoint(x: cx, y: cy)) {
    let x = cx - 150 + CGFloat(i) * 14, y = cy - 120 + CGFloat(i) * 22
    fill(rounded(x, y, 280, 150, 18), i == 3 ? w(0.92) : w(0.16 + CGFloat(i) * 0.08)); stroke(rounded(x, y, 280, 150, 18), w(0.5), 2)
    if i == 3 { for row in 0..<3 { fill(rounded(x + 26, y + 104 - CGFloat(row) * 32, row == 0 ? 140 : rng.range(150, 220), 12, 6), row == 0 ? hot : hot.withAlphaComponent(0.3)) } }
   }
  }
  markTile(mark, cx + 160, cy - 125, 96)
 case "download":
  fill(rounded(cx - 150, cy - 160, 300, 70, 20), w(0.2)); stroke(rounded(cx - 150, cy - 160, 300, 70, 20), w(0.6), 3)
  fill(rounded(cx - 130, cy - 133, 260 * rng.range(0.45, 0.85), 16, 8), hot)
  let arrow = line([NSPoint(x: cx - 42, y: cy + 150), NSPoint(x: cx + 42, y: cy + 150), NSPoint(x: cx + 42, y: cy + 10), NSPoint(x: cx + 100, y: cy + 10), NSPoint(x: cx, y: cy - 75), NSPoint(x: cx - 100, y: cy + 10), NSPoint(x: cx - 42, y: cy + 10), NSPoint(x: cx - 42, y: cy + 150)])
  fill(arrow, w(0.9))
  markTile(mark, cx + 170, cy + 110, 96)
 case "photo":
  for i in 0..<2 {
   rotated(i == 0 ? -8 : 4, around: NSPoint(x: cx, y: cy)) {
    let x = cx - 170 + CGFloat(i) * 30, y = cy - 130 + CGFloat(i) * 10
    fill(rounded(x, y, 320, 240, 16), i == 1 ? w(0.95) : w(0.22))
    if i == 1 {
     fill(rounded(x + 16, y + 16, 288, 208, 10), hot.withAlphaComponent(0.25))
     fill(line([NSPoint(x: x + 16, y: y + 16), NSPoint(x: x + 120, y: y + 140), NSPoint(x: x + 190, y: y + 70), NSPoint(x: x + 230, y: y + 110), NSPoint(x: x + 304, y: y + 30), NSPoint(x: x + 304, y: y + 16)]), hot)
     fill(oval(x + 240, y + 175, 22, 22), alt)
    }
   }
  }
  markTile(mark, cx + 170, cy + 130, 92)
 case "chart":
  let x = cx - 190, y = cy - 140
  fill(rounded(x, y, 380, 280, 20), w(0.14)); stroke(rounded(x, y, 380, 280, 20), w(0.4), 2)
  var points: [NSPoint] = []
  for i in 0..<8 {
   let h = rng.range(40, 190)
   fill(rounded(x + 30 + CGFloat(i) * 42, y + 30, 26, h, 6), w(0.22))
   points.append(NSPoint(x: x + 43 + CGFloat(i) * 42, y: y + 40 + h))
  }
  stroke(line(points), hot, 5)
  points.forEach { fill(oval($0.x, $0.y, 7, 7), white) }
  markTile(mark, x + 380, y + 280, 92)
 case "book":
  for side: CGFloat in [-1, 1] {
   let page = NSBezierPath()
   page.move(to: NSPoint(x: cx, y: cy - 120)); page.curve(to: NSPoint(x: cx + side * 190, y: cy - 110), controlPoint1: NSPoint(x: cx + side * 60, y: cy - 150), controlPoint2: NSPoint(x: cx + side * 130, y: cy - 140))
   page.line(to: NSPoint(x: cx + side * 190, y: cy + 140)); page.curve(to: NSPoint(x: cx, y: cy + 130), controlPoint1: NSPoint(x: cx + side * 130, y: cy + 110), controlPoint2: NSPoint(x: cx + side * 60, y: cy + 100)); page.close()
   fill(page, w(side < 0 ? 0.9 : 0.75)); stroke(page, w(0.9), 2)
   for row in 0..<5 { let ly = cy + 70 - CGFloat(row) * 38; stroke(line([NSPoint(x: cx + side * 30, y: ly), NSPoint(x: cx + side * rng.range(120, 160), y: ly + 4)]), hot.withAlphaComponent(0.45), 6) }
  }
  fill(rounded(cx - 4, cy - 130, 8, 262, 4), hot)
  markTile(mark, cx + 175, cy + 135, 88)
 case "tree":
  let root = NSPoint(x: cx - 170, y: cy + 140)
  fill(rounded(root.x - 10, root.y - 18, 150, 36, 12), w(0.9))
  var row: CGFloat = 0
  for (depth, count) in [(1, 2), (2, 2), (1, 1), (2, 1)] {
   row += 1
   let y = root.y - row * 62, x = root.x + CGFloat(depth) * 60
   stroke(line([NSPoint(x: x - 40, y: y + 44), NSPoint(x: x - 40, y: y), NSPoint(x: x - 8, y: y)]), w(0.6), 3)
   for c in 0..<count { fill(rounded(x + CGFloat(c) * 128, y - 16, rng.range(90, 118), 32, 10), c == 0 && depth == 2 ? hot : w(0.22 + CGFloat(depth) * 0.08)) }
  }
  markTile(mark, cx + 170, cy - 120, 96)
 case "search":
  stroke(oval(cx - 40, cy + 30, 115, 115), white, 16)
  fill(oval(cx - 40, cy + 30, 107, 107), w(0.12))
  stroke(line([NSPoint(x: cx + 42, y: cy - 52), NSPoint(x: cx + 140, y: cy - 150)]), white, 30)
  for row in 0..<3 { fill(rounded(cx - 110, cy + 60 - CGFloat(row) * 34, rng.range(90, 140), 12, 6), row == 1 ? hot : w(0.6)) }
  markTile(mark, cx + 170, cy + 130, 92)
 case "lock":
  let shackle = NSBezierPath(); shackle.appendArc(withCenter: NSPoint(x: cx - 10, y: cy + 40), radius: 82, startAngle: 0, endAngle: 180)
  shackle.line(to: NSPoint(x: cx - 92, y: cy - 10)); shackle.move(to: NSPoint(x: cx + 72, y: cy + 40)); shackle.line(to: NSPoint(x: cx + 72, y: cy - 10))
  stroke(shackle, w(0.9), 24)
  fill(rounded(cx - 140, cy - 160, 260, 190, 30), w(0.92))
  fill(oval(cx - 10, cy - 50, 24, 24), hot); fill(rounded(cx - 20, cy - 120, 20, 70, 10), hot)
  markTile(mark, cx + 175, cy + 130, 92)
 case "flame":
  for row in 0..<5 {
   var x = cx - 200 + CGFloat(row) * 14
   let limit = cx + 200 - CGFloat(row) * 30
   while x < limit {
    let span = min(rng.range(50, 170), limit - x)
    let h = (hot.hueComponent + CGFloat(row) * 0.03 + rng.range(-0.02, 0.02) + 1).truncatingRemainder(dividingBy: 1)
    fill(rounded(x, cy - 140 + CGFloat(row) * 50, span - 6, 42, 8), NSColor(deviceHue: h, saturation: 0.75, brightness: 0.98 - CGFloat(row) * 0.05, alpha: 0.95))
    x += span
   }
  }
  markTile(mark, cx + 160, cy + 140, 92)
 case "phone":
  for (i, offset) in [CGFloat(-95), 85].enumerated() {
   let x = cx + offset - 70, y = cy - 150
   fill(rounded(x, y, 140, 290, 26), i == 0 ? w(0.92) : w(0.2)); stroke(rounded(x, y, 140, 290, 26), w(0.7), 3)
   fill(rounded(x + 50, y + 268, 40, 8, 4), i == 0 ? hot.withAlphaComponent(0.6) : w(0.5))
   for row in 0..<4 { fill(rounded(x + 16, y + 210 - CGFloat(row) * 52, 108, 40, 10), i == 0 ? hot.withAlphaComponent(row == 0 ? 0.9 : 0.25) : w(0.25)) }
  }
  markTile(mark, cx + 175, cy - 130, 88)
 case "keys":
  for r in 0..<3 { for c in 0..<(4 - r % 2) {
   let x = cx - 200 + CGFloat(c) * 100 + CGFloat(r % 2) * 50, y = cy + 60 - CGFloat(r) * 96
   let pressed = (r * 4 + c) % 5 == Int(rng.range(0, 5))
   fill(rounded(x, y, 88, 84, 18), pressed ? hot : w(0.18)); stroke(rounded(x, y, 88, 84, 18), w(0.6), 2.5)
   fill(rounded(x + 8, y + 12, 72, 64, 12), pressed ? w(0.25) : w(0.08))
  } }
  markTile(mark, cx + 175, cy + 150, 90)
 case "mail":
  let x = cx - 180, y = cy - 120
  fill(rounded(x, y, 340, 230, 22), w(0.9))
  stroke(line([NSPoint(x: x + 14, y: y + 216), NSPoint(x: x + 170, y: y + 100), NSPoint(x: x + 326, y: y + 216)]), hot, 7)
  stroke(line([NSPoint(x: x + 14, y: y + 14), NSPoint(x: x + 130, y: y + 120)]), hot.withAlphaComponent(0.5), 4)
  stroke(line([NSPoint(x: x + 326, y: y + 14), NSPoint(x: x + 210, y: y + 120)]), hot.withAlphaComponent(0.5), 4)
  markTile(mark, x + 340, y + 230, 96)
 case "sketch":
  // Hand-drawn boxes joined by arrows: each box outline is drawn twice with a little jitter, like a pen sketch.
  let boxes: [(CGFloat, CGFloat, CGFloat, CGFloat, Bool)] = [(cx - 250, cy + 60, 140, 80, false), (cx - 60, cy + 100, 150, 80, true), (cx - 170, cy - 120, 150, 80, false), (cx + 40, cy - 70, 130, 80, false)]
  for (x, y, bw, bh, hl) in boxes {
   fill(rounded(x + 6, y + 6, bw - 12, bh - 12, 8), hl ? hot.withAlphaComponent(0.85) : w(0.16))
   for _ in 0..<2 {
    var j: [CGFloat] = []
    for _ in 0..<8 { j.append(rng.range(-5, 5)) }
    stroke(line([NSPoint(x: x + j[0], y: y + j[1]), NSPoint(x: x + bw + j[2], y: y + j[3]), NSPoint(x: x + bw + j[4], y: y + bh + j[5]), NSPoint(x: x + j[6], y: y + bh + j[7]), NSPoint(x: x + j[0], y: y + j[1])]), w(0.95), 4)
   }
  }
  for (a, b) in [(NSPoint(x: cx - 106, y: cy + 104), NSPoint(x: cx - 66, y: cy + 138)), (NSPoint(x: cx - 180, y: cy + 54), NSPoint(x: cx - 112, y: cy - 34)), (NSPoint(x: cx - 14, y: cy - 82), NSPoint(x: cx + 34, y: cy - 34))] {
   let mid = NSPoint(x: (a.x + b.x) / 2 + rng.range(-18, 18), y: (a.y + b.y) / 2 + rng.range(-18, 18))
   let curve = NSBezierPath(); curve.move(to: a); curve.curve(to: b, controlPoint1: mid, controlPoint2: mid)
   stroke(curve, w(0.85), 4)
   let angle = atan2(b.y - mid.y, b.x - mid.x)
   for side in [CGFloat(0.5), -0.5] { stroke(line([b, NSPoint(x: b.x - 22 * cos(angle + side), y: b.y - 22 * sin(angle + side))]), w(0.85), 4) }
  }
  // A pencil finishing the last stroke.
  let pencil = NSBezierPath(); pencil.move(to: NSPoint(x: cx + 160, y: cy - 160)); pencil.line(to: NSPoint(x: cx + 210, y: cy - 110)); pencil.line(to: NSPoint(x: cx + 192, y: cy - 92)); pencil.line(to: NSPoint(x: cx + 142, y: cy - 142)); pencil.close()
  fill(pencil, alt.withAlphaComponent(0.95))
  fill(line([NSPoint(x: cx + 160, y: cy - 160), NSPoint(x: cx + 130, y: cy - 170), NSPoint(x: cx + 142, y: cy - 142)]), w(0.95))
  markTile(mark, cx + 175, cy + 150, 92)
 case "links":
  // A terminal whose "file:line" locations are underlined links, one hovered under a pointer.
  let x = cx - 200, y = cy - 140
  fill(rounded(x, y, 400, 270, 18), NSColor.black.withAlphaComponent(0.3)); stroke(rounded(x, y, 400, 270, 18), w(0.35), 2)
  for (i, c) in [NSColor(deviceRed: 1, green: 0.37, blue: 0.34, alpha: 1), NSColor(deviceRed: 1, green: 0.74, blue: 0.18, alpha: 1), NSColor(deviceRed: 0.16, green: 0.79, blue: 0.25, alpha: 1)].enumerated() {
   fill(oval(x + 26 + CGFloat(i) * 22, y + 246, 7, 7), c)
  }
  let rows: [(String, Bool)] = [("Traceback (most recent call last):", false), ("  app/main.py:42", true), ("panic: index out of range", false), ("  server/user.go:118", true)]
  for (row, item) in rows.enumerated() {
   let ly = y + 192 - CGFloat(row) * 40
   if item.1 {
    let hovered = row == 3
    draw(item.0, at: NSPoint(x: x + 26, y: ly - 10), size: 17, color: hovered ? hot : w(0.92), weight: .semibold, mono: true)
    let tw = width(item.0, size: 17, weight: .semibold, mono: true) - width("  ", size: 17, weight: .semibold, mono: true)
    fill(rounded(x + 26 + width("  ", size: 17, weight: .semibold, mono: true), ly - 14, tw, 3, 1.5), hovered ? hot : w(0.7))
   } else {
    draw(item.0, at: NSPoint(x: x + 26, y: ly - 9), size: 13, color: w(0.5), weight: .regular, mono: true)
   }
  }
  // Pointer over the hovered link.
  let px = x + 236, py = y + 62
  let pointer = line([NSPoint(x: px, y: py), NSPoint(x: px, y: py - 46), NSPoint(x: px + 12, y: py - 34), NSPoint(x: px + 22, y: py - 54), NSPoint(x: px + 30, y: py - 50), NSPoint(x: px + 20, y: py - 30), NSPoint(x: px + 36, y: py - 30)])
  pointer.close()
  fill(pointer, white); stroke(pointer, NSColor.black.withAlphaComponent(0.55), 2)
  markTile(mark, x + 400, y + 270, 100)
 case "remote":
  // A remote desktop: a large monitor showing windows, reached from a small local laptop.
  let mx = cx - 110, my = cy - 95, mw: CGFloat = 320, mh: CGFloat = 220
  fill(rounded(mx + mw / 2 - 30, my - 46, 60, 46, 6), w(0.35))
  fill(rounded(mx + mw / 2 - 80, my - 56, 160, 14, 7), w(0.5))
  fill(rounded(mx, my, mw, mh, 18), w(0.16)); stroke(rounded(mx, my, mw, mh, 18), w(0.75), 3)
  let sx = mx + 16, sy = my + 16, sw = mw - 32, sh = mh - 32
  fill(rounded(sx, sy, sw, sh, 8), w(0.12))
  fill(rounded(sx, sy, sw, 22, 6), w(0.38))
  for k in 0..<4 { fill(rounded(sx + 10 + CGFloat(k) * 26, sy + 5, 16, 12, 3), k == 0 ? hot : w(0.6)) }
  fill(rounded(sx + 18, sy + 44, 160, 116, 8), w(0.9))
  fill(rounded(sx + 18, sy + 142, 160, 18, 6), hot)
  for row in 0..<3 { fill(rounded(sx + 32, sy + 116 - CGFloat(row) * 24, 122 - CGFloat(row) * 26, 10, 5), hot.withAlphaComponent(0.35)) }
  fill(rounded(sx + 134, sy + 70, 138, 104, 8), w(0.32)); stroke(rounded(sx + 134, sy + 70, 138, 104, 8), w(0.7), 2)
  fill(rounded(sx + 134, sy + 158, 138, 16, 6), alt.withAlphaComponent(0.85))
  // The local laptop and its link to the remote screen.
  let lx = cx - 330, ly = cy - 190
  fill(rounded(lx, ly + 18, 150, 96, 12), w(0.92)); stroke(rounded(lx, ly + 18, 150, 96, 12), w(0.75), 3)
  fill(rounded(lx + 12, ly + 30, 126, 72, 6), hot.withAlphaComponent(0.85))
  fill(rounded(lx + 22, ly + 40, 58, 36, 4), w(0.85))
  fill(rounded(lx - 16, ly, 182, 18, 8), w(0.6))
  let link = line([NSPoint(x: lx + 150, y: ly + 66), NSPoint(x: mx + 36, y: ly + 66), NSPoint(x: mx + 36, y: my - 10)])
  link.setLineDash([12, 10], count: 2, phase: 0)
  stroke(link, w(0.85), 4)
  fill(oval(lx + 150, ly + 66, 8, 8), hot); fill(oval(mx + 36, my - 10, 8, 8), hot)
  markTile(mark, cx + 175, cy - 160, 88)
 case "containers":
  // Stacked shipping containers: ribbed boxes, one in the plugin color.
  let rows: [[CGFloat]] = [[-210, -70, 70], [-140, 0], [-70]]
  for (r, xs) in rows.enumerated() {
   for (i, dx) in xs.enumerated() {
    let bx = cx + dx, by = cy - 150 + CGFloat(r) * 100, bw: CGFloat = 132, bh: CGFloat = 88
    let highlighted = r == 1 && i == 1
    fill(rounded(bx, by, bw, bh, 10), highlighted ? hot : w(0.22 + CGFloat(r) * 0.06))
    stroke(rounded(bx, by, bw, bh, 10), w(0.7), 2.5)
    for k in 1..<6 { let rx = bx + CGFloat(k) * bw / 6; stroke(line([NSPoint(x: rx, y: by + 12), NSPoint(x: rx, y: by + bh - 12)]), highlighted ? w(0.55) : w(0.35), 3) }
   }
  }
  // Water line under the stack.
  for k in 0..<3 {
   let wave = NSBezierPath(); let wy = cy - 172 - CGFloat(k) * 18
   wave.move(to: NSPoint(x: cx - 250, y: wy))
   for step in 0..<6 { let sx = cx - 250 + CGFloat(step) * 80; wave.curve(to: NSPoint(x: sx + 80, y: wy), controlPoint1: NSPoint(x: sx + 20, y: wy + 10), controlPoint2: NSPoint(x: sx + 60, y: wy - 10)) }
   stroke(wave, w(0.5 - CGFloat(k) * 0.14), 3)
  }
  markTile(mark, cx + 170, cy + 130, 100)
 case "flowchart":
  // A small flowchart: start, a step, a decision with two branches, and an end.
  let start = NSPoint(x: cx - 120, y: cy + 150), step = NSPoint(x: cx - 120, y: cy + 50), decision = NSPoint(x: cx - 120, y: cy - 70)
  let yes = NSPoint(x: cx + 90, y: cy - 70), no = NSPoint(x: cx - 120, y: cy - 175)
  func connector(_ points: [NSPoint]) {
   stroke(line(points), w(0.8), 4)
   let a = points[points.count - 2], b = points[points.count - 1]
   let angle = atan2(b.y - a.y, b.x - a.x)
   fill(line([b, NSPoint(x: b.x - 16 * cos(angle - 0.45), y: b.y - 16 * sin(angle - 0.45)), NSPoint(x: b.x - 16 * cos(angle + 0.45), y: b.y - 16 * sin(angle + 0.45))]), w(0.8))
  }
  connector([NSPoint(x: start.x, y: start.y - 26), NSPoint(x: step.x, y: step.y + 32)])
  connector([NSPoint(x: step.x, y: step.y - 32), NSPoint(x: decision.x, y: decision.y + 52)])
  connector([NSPoint(x: decision.x + 72, y: decision.y), NSPoint(x: yes.x - 76, y: yes.y)])
  connector([NSPoint(x: decision.x, y: decision.y - 52), NSPoint(x: no.x, y: no.y + 30)])
  connector([NSPoint(x: yes.x, y: yes.y + 32), NSPoint(x: yes.x, y: step.y), NSPoint(x: step.x + 92, y: step.y)])
  fill(rounded(start.x - 78, start.y - 26, 156, 52, 26), w(0.9))
  fill(rounded(step.x - 92, step.y - 32, 184, 64, 10), w(0.9))
  let diamond = line([NSPoint(x: decision.x, y: decision.y + 52), NSPoint(x: decision.x + 72, y: decision.y), NSPoint(x: decision.x, y: decision.y - 52), NSPoint(x: decision.x - 72, y: decision.y)]); diamond.close()
  fill(diamond, hot)
  fill(rounded(yes.x - 76, yes.y - 32, 152, 64, 10), alt.withAlphaComponent(0.9))
  fill(rounded(no.x - 78, no.y - 30, 156, 60, 30), w(0.55))
  for (p, len) in [(start, CGFloat(70)), (step, 100), (yes, 80), (no, 70)] { fill(rounded(p.x - len / 2, p.y - 5, len, 10, 5), NSColor.black.withAlphaComponent(0.18)) }
  draw("?", at: decision, size: 34, color: white, weight: .heavy, centered: true)
  markTile(mark, cx + 175, cy + 150, 92)
 case "cloud":
  let cloud = NSBezierPath()
  for (dx, dy, r) in [(CGFloat(-90), CGFloat(-10), CGFloat(70)), (-10, 40, 95), (90, 0, 75)] { cloud.append(oval(cx + dx, cy + dy, r, r)) }
  cloud.append(rounded(cx - 160, cy - 80, 320, 80, 40))
  fill(cloud, w(0.88))
  for i in 0..<3 { fill(rounded(cx - 120 + CGFloat(i) * 90, cy - 160, 60, 60, 12), i == 1 ? hot : w(0.3)); stroke(line([NSPoint(x: cx - 90 + CGFloat(i) * 90, y: cy - 100), NSPoint(x: cx - 90 + CGFloat(i) * 90, y: cy - 80)]), w(0.6), 3) }
  markTile(mark, cx + 175, cy + 140, 90)
 case "businesscard":
  // A QR back card behind a printed front card (brand band, name, contact rows) with crop marks.
  rotated(7, around: NSPoint(x: cx, y: cy)) {
   let x = cx - 70, y = cy - 6
   fill(rounded(x, y, 270, 162, 12), w(0.26)); stroke(rounded(x, y, 270, 162, 12), w(0.6), 2)
   let m: CGFloat = 8, qx = x + 162, qy = y + 42
   fill(rounded(qx - 8, qy - 8, m * 10 + 16, m * 10 + 16, 8), w(0.95))
   for row in 0..<10 { for col in 0..<10 where !((row < 3 || row > 6) && (col < 3 || col > 6) && !(row > 6 && col > 6)) {
    if rng.next() < 0.5 { fill(NSBezierPath(rect: NSRect(x: qx + CGFloat(col) * m, y: qy + CGFloat(row) * m, width: m, height: m)), tileFill) }
   } }
   for (fx, fy) in [(qx, qy), (qx, qy + 7 * m), (qx + 7 * m, qy + 7 * m)] {
    fill(NSBezierPath(rect: NSRect(x: fx, y: fy, width: 3 * m, height: 3 * m)), tileFill)
    fill(NSBezierPath(rect: NSRect(x: fx + m / 2, y: fy + m / 2, width: 2 * m, height: 2 * m)), w(0.95))
    fill(NSBezierPath(rect: NSRect(x: fx + m, y: fy + m, width: m, height: m)), tileFill)
   }
   for row in 0..<3 { fill(rounded(x + 22, y + 112 - CGFloat(row) * 24, row == 0 ? 112 : rng.range(80, 120), 9, 4.5), w(row == 0 ? 0.9 : 0.6)) }
  }
  rotated(-5, around: NSPoint(x: cx, y: cy)) {
   let x = cx - 215, y = cy - 150, cw: CGFloat = 330, ch: CGFloat = 198
   fill(rounded(x + 6, y - 8, cw, ch, 10), NSColor.black.withAlphaComponent(0.14))
   fill(rounded(x, y, cw, ch, 10), w(0.97))
   let band = NSBezierPath(roundedRect: NSRect(x: x, y: y, width: 40, height: ch), xRadius: 10, yRadius: 10)
   NSGraphicsContext.saveGraphicsState(); NSBezierPath(rect: NSRect(x: x, y: y, width: 22, height: ch)).addClip(); fill(band, hot); NSGraphicsContext.restoreGraphicsState()
   fill(oval(x + 72, y + ch - 52, 22, 22), hot.withAlphaComponent(0.9))
   fill(rounded(x + 108, y + ch - 50, 150, 18, 6), tileFill)
   fill(rounded(x + 108, y + ch - 74, 96, 10, 5), hot.withAlphaComponent(0.75))
   for row in 0..<3 {
    let ry = y + 78 - CGFloat(row) * 26
    fill(oval(x + 60, ry + 5, 7, 7), hot)
    fill(rounded(x + 76, ry, [CGFloat(132), 168, 118][row], 9, 4.5), tileFill.withAlphaComponent(0.3))
   }
   // Crop marks just outside each corner.
   for (px, py, dx, dy) in [(x, y, CGFloat(-1), CGFloat(-1)), (x + cw, y, 1, -1), (x, y + ch, -1, 1), (x + cw, y + ch, 1, 1)] {
    stroke(line([NSPoint(x: px + dx * 8, y: py), NSPoint(x: px + dx * 28, y: py)]), w(0.85), 2)
    stroke(line([NSPoint(x: px, y: py + dy * 8), NSPoint(x: px, y: py + dy * 28)]), w(0.85), 2)
   }
  }
  markTile(mark, cx + 175, cy - 150, 92)
 default:
  for i in 0..<4 { let s = 300 - CGFloat(i) * 64; stroke(rounded(cx - s / 2, cy - s / 2, s, s, s * 0.24), w(0.2 + CGFloat(i) * 0.15), 3) }
  markTile(mark, cx, cy, 110)
 }
}

let scale = 2
// JPEG: a 1920×960 gradient PNG is several MB, far above the desktop's 512 KiB cover limit.
func encode(_ bitmap: NSBitmapImageRep) -> Data {
 bitmap.representation(using: .jpeg, properties: [.compressionFactor: 0.88])!
}

for (slug, title, subtitle, mark, hex, labels) in plugins where CommandLine.arguments.count == 1 || CommandLine.arguments.dropFirst().contains(slug) {
 guard let kind = category(slug) else { print("\(slug): no plugin.json, skipped"); continue }
 var rng = Seeded(slug)
 // Backgrounds use the MinCode accent (--app-accent #5D87FF); the plugin's own color only marks highlights.
 let hue = brandAccent.hueComponent, own = color(hex).usingColorSpace(.deviceRGB)!
 let deep = NSColor(deviceHue: hue, saturation: 0.58, brightness: 0.82, alpha: 1)
 let middle = NSColor(deviceHue: hue, saturation: 0.26, brightness: 1.0, alpha: 1)
 let hot = NSColor(deviceHue: own.hueComponent, saturation: max(own.saturationComponent, 0.55) * 0.85, brightness: 1, alpha: 1)
 let alt = NSColor(deviceHue: (hue + 0.06).truncatingRemainder(dividingBy: 1), saturation: 0.45, brightness: 1, alpha: 1)
 tileFill = NSColor(deviceHue: hue, saturation: 0.72, brightness: 0.62, alpha: 0.95)

 // Drawn in 960×480 points at 2× so covers stay sharp on Retina displays.
 let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 960 * scale, pixelsHigh: 480 * scale, bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
 bitmap.size = NSSize(width: 960, height: 480)
 NSGraphicsContext.saveGraphicsState()
 NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
 NSGradient(colors: [deep, middle])!.draw(in: NSRect(x: 0, y: 0, width: 960, height: 480), angle: CGFloat(rng.range(10, 35)))
 for (center, radius, c, alpha) in [(NSPoint(x: 760, y: rng.range(260, 380)), CGFloat(420), hot, CGFloat(0.5)), (NSPoint(x: rng.range(820, 960), y: 30), CGFloat(320), alt, CGFloat(0.35))] {
  NSGradient(colors: [c.withAlphaComponent(alpha), c.withAlphaComponent(0)])!.draw(fromCenter: center, radius: 0, toCenter: center, radius: radius, options: [])
 }
 for gx in stride(from: CGFloat(500), through: 960, by: 24) { for gy in stride(from: CGFloat(12), through: 480, by: 24) { fill(oval(gx, gy, 1.2, 1.2), w(0.08)) } }

 illustration(kind, mark: mark, hot: hot, alt: alt, rng: &rng)

 fill(rounded(48, 414, 30, 5, 2.5), hot)
 draw("MINCODE  ·  PLUGIN", at: NSPoint(x: 90, y: 405), size: 14, color: w(0.65), weight: .medium, mono: true)
 var titleSize: CGFloat = 58
 while width(title, size: titleSize, weight: .heavy) > 430 && titleSize > 30 { titleSize -= 2 }
 draw(title, at: NSPoint(x: 46, y: 300), size: titleSize, color: white, weight: .heavy)
 var subtitleSize: CGFloat = 15
 while width(subtitle, size: subtitleSize, weight: .medium, mono: true) > 430 && subtitleSize > 10 { subtitleSize -= 1 }
 draw(subtitle, at: NSPoint(x: 48, y: 266), size: subtitleSize, color: w(0.78), weight: .medium, mono: true)
 var chipX: CGFloat = 48, chipY: CGFloat = 72
 for label in labels {
  let wide = width(label, size: 13, weight: .semibold) + 30
  if chipX + wide > 470 { chipX = 48; chipY -= 42 }
  fill(rounded(chipX, chipY, wide, 32, 16), w(0.12)); stroke(rounded(chipX, chipY, wide, 32, 16), w(0.4), 1.5)
  draw(label, at: NSPoint(x: chipX + 15, y: chipY + 8), size: 13, color: white, weight: .semibold)
  chipX += wide + 10
 }
 NSGraphicsContext.restoreGraphicsState()
 let data = encode(bitmap)
 // The desktop drops covers above 512 KiB (app/backend/plugin/cover.go maxCoverBytes).
 guard data.count <= 500 * 1024 else { fatalError("\(slug): cover is \(data.count) bytes, above the 512 KiB desktop limit") }
 if let preview = ProcessInfo.processInfo.environment["COVER_OUT"] {
  try FileManager.default.createDirectory(atPath: "\(preview)/\(slug)", withIntermediateDirectories: true)
  try data.write(to: URL(fileURLWithPath: "\(preview)/\(slug)/cover.jpg"))
 } else {
  try data.write(to: URL(fileURLWithPath: "official-plugins/\(slug)/assets/cover.jpg"))
  // Point the manifest at the JPEG and drop the PNG it replaces.
  let manifest = "official-plugins/\(slug)/plugin.json"
  let text = try String(contentsOfFile: manifest, encoding: .utf8)
  let updated = text.replacingOccurrences(of: "\"cover\": \"assets/cover.png\"", with: "\"cover\": \"assets/cover.jpg\"")
  if updated != text { try updated.write(toFile: manifest, atomically: true, encoding: .utf8) }
  try? FileManager.default.removeItem(atPath: "official-plugins/\(slug)/assets/cover.png")
 }
 print("\(slug): \(kind) \(data.count) bytes")
}
