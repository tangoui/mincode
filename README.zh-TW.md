# MinCode

[English](README.md) · [简体中文](README.zh-CN.md) · **繁體中文** · [日本語](README.ja.md)

以 [Tango](https://github.com/tangoui/tango) 打造的桌面開發工作台：終端機、SSH/SFTP、資料庫、HTTP 除錯、筆記、AI 助理與 AI CLI 工作階段，全都在同一個視窗裡。

這個儲存庫是 MinCode 的開源精簡版，也是一個「中等規模的 Tango 應用程式長什麼樣子」的完整範例：Go 後端加上 Vue 前端，由 Tango 的系統 WebView 承載，以 `tan` 打包，並透過 GitHub Releases 自動更新。

**Tango**：[GitHub 儲存庫](https://github.com/tangoui/tango) · [文件](https://tangoui.github.io/tango/)

## 下載

請依作業系統與處理器選擇安裝檔。[GitHub Releases](https://github.com/tangoui/mincode/releases/latest)

| 作業系統 | 架構 | 安裝檔 |
|---|---|---|
| macOS | Apple 晶片（ARM64）、Intel（x64） | `.dmg` |
| Windows | x64、ARM64 | `-setup.exe` |
| Linux | x64、ARM64 | `.tar.gz`、`.AppImage`、`.deb`、`.rpm` |

在 Linux 上，可將 `.tar.gz` 解壓縮後直接從目錄執行，或為 `.AppImage` 加上執行權限後啟動。Debian、Ubuntu 等發行版可使用 `.deb`，Fedora、openSUSE 等發行版可使用 `.rpm`。以 AppImage、deb 或 rpm 安裝的版本，可在設定中檢查更新，有新版本時可開啟 GitHub Releases 下載對應的安裝檔。

## 功能

| 模組 | 說明 |
|---|---|
| 儀表板 | 常用連線、最近的工作階段與快捷入口 |
| 終端機 | 本機終端機，支援多分頁與分割畫面；預設從家目錄啟動，也可選擇資料夾 |
| SSH / SFTP | 主機管理、金鑰與跳板機、遠端終端機與檔案傳輸 |
| 資料庫 | MySQL、PostgreSQL、SQLite、SQL Server、Redis、MongoDB、Elasticsearch、Cassandra、Neo4j、InfluxDB、TDengine、Trino 等；SQL 編輯器、結果表格、資料表結構比對、匯入匯出、備份 |
| HTTP | 請求除錯、集合、環境變數、cURL 匯入、程式碼產生、WebSocket / SSE / Socket.IO |
| 筆記 | Markdown 筆記 |
| CC 工作階段 | 瀏覽 Claude Code、Codex 等 AI CLI 的本機工作階段紀錄 |
| AI 助理 | 接入自己的模型（OpenAI 相容、Anthropic 等）或本機 Agent（ACP），可呼叫終端機、資料庫、HTTP 等工具 |
| 設定 | 外觀、快速鍵、終端機、AI、資料匯入匯出、檢查更新 |

## 系統需求

- macOS 12 以上、Windows 10 以上（x64）或 Windows 11（ARM64）、Linux（GTK 3 與 WebKitGTK 4.1）
- 從原始碼建置：Go（版本見 `go.mod`）、Node.js 26、pnpm 11（見 `package.json` 的 `packageManager`）、C 編譯器（需啟用 cgo）

## 開發

```sh
pnpm install --frozen-lockfile
go install github.com/tangoui/tango/cmd/tan@latest   # 版本盡量與 go.mod 中的 tango 一致
pnpm dev                                             # tan dev：啟動桌面端並即時更新前端
```

## 建置與測試

```sh
(cd vue-app && pnpm build)                           # 前端產物寫入 frontend/dist，由 main.go 內嵌
CGO_ENABLED=1 go build -tags production,nocoredrivers .
pnpm package:desktop                                 # tan package，安裝檔輸出到 dist/

CGO_ENABLED=1 go test ./internal/... ./backend/...
(cd vue-app && pnpm lint && pnpm typecheck && pnpm test && pnpm test:components)
CGO_ENABLED=1 TANGO_LAUNCH_BACKGROUND=1 go run ./smoketest
CGO_ENABLED=1 TANGO_E2E=1 go test ./e2e -run E2E -v
```

E2E 測試每次都以獨立的應用程式名稱、ID 與暫存資料目錄啟動（`MINCODE_APP_NAME`、`MINCODE_APP_ID`、`MINCODE_DATA_DIR`），不會讀寫你平常使用的資料。

## 發布與自動更新

推送 `vX.Y.Z` 標籤後，GitHub Actions 會在六種作業系統與架構組合上打包，產生簽署過的更新清單與增量修補檔，並建立 Release 草稿。草稿發布後，已安裝的用戶端才會收到更新。應用程式內的更新由 Tango 的 `service/updater` 完成：它會驗證清單簽章與檔案摘要，優先使用增量修補檔，必要時下載完整套件。新版本啟動後會確認更新，確認失敗則自動復原。

首次發布前，請用 `tan keygen` 產生金鑰，並將私鑰存入儲存庫 Secret `TANGO_UPDATE_KEY`。完整步驟見 [docs/release.md](docs/release.md)。

安裝檔預設未做程式碼簽署。macOS 首次開啟時若被阻擋，可在「系統設定 → 隱私權與安全性」中選擇「強制打開」，或執行 `xattr -dr com.apple.quarantine /Applications/MinCode.app`。

## 目錄

| 目錄 | 內容 |
|---|---|
| `main.go` | 桌面入口：視窗、系統匣、選單、單一執行個體、更新確認、後端啟動 |
| `internal/host/` | 宿主銜接層：Tango IPC 處理器、後端串流轉接、事件依序傳送 |
| `backend/` | 依業務領域組織的 Go 命令與服務，不依賴 Tango |
| `vue-app/` | Vue 3 + TypeScript 前端 |
| `frontend/dist/` | 前端建置產物（不納入版本控制），由 `main.go` 以 `//go:embed` 內嵌 |
| `smoketest/`、`e2e/` | 真實視窗冒煙測試與整機自動化測試（視窗不搶前景） |
| `tools/e2e-sshd/` | E2E 使用的本機 SSH 服務 |
| `.github/workflows/` | CI、打包發布、更新清單定期重新簽署 |
| `tan.json` | Tango 工具鏈設定 |

## 授權

[AGPL-3.0](LICENSE)。
