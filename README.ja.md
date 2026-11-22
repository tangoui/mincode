# MinCode

[English](README.md) · [简体中文](README.zh-CN.md) · [繁體中文](README.zh-TW.md) · **日本語**

[Tango](https://github.com/tangoui/tango) で作られたデスクトップ開発ワークスペースです。ターミナル、SSH/SFTP、データベース、HTTP デバッグ、ノート、AI アシスタント、AI CLI のセッションを 1 つのウィンドウにまとめています。

このリポジトリは MinCode のオープンソース版であり、中規模の Tango アプリの完全な実例でもあります。Go のバックエンドと Vue のフロントエンドを Tango のシステム WebView で動かし、`tan` でパッケージ化し、GitHub Releases から自動更新します。

## ダウンロード

OS とプロセッサーに合ったインストーラーを選んでください。[GitHub Releases](https://github.com/tangoui/mincode/releases/latest)

| OS | アーキテクチャ | インストーラー |
|---|---|---|
| macOS | Apple シリコン（ARM64）、Intel（x64） | `.dmg` |
| Windows | x64、ARM64 | `-setup.exe` |
| Linux | x64、ARM64 | `.tar.gz`、`.AppImage`、`.deb`、`.rpm` |

Linux では、`.tar.gz` を展開してそのディレクトリから実行するか、`.AppImage` に実行権限を付けて起動します。Ubuntu などの Debian 系では `.deb`、Fedora や openSUSE などでは `.rpm` を使えます。AppImage、deb、rpm でインストールした場合も設定から更新を確認でき、新しいバージョンがあれば GitHub Releases を開いてインストーラーをダウンロードできます。

## 機能

| モジュール | 内容 |
|---|---|
| ダッシュボード | よく使う接続、最近のセッション、ショートカット |
| ターミナル | タブと分割に対応したローカルターミナル。ホームフォルダーで起動し、別のフォルダーも選択可能 |
| SSH / SFTP | ホスト管理、鍵と踏み台サーバー、リモートターミナル、ファイル転送 |
| データベース | MySQL、PostgreSQL、SQLite、SQL Server、Redis、MongoDB、Elasticsearch、Cassandra、Neo4j、InfluxDB、TDengine、Trino など。SQL エディター、結果グリッド、スキーマ比較、インポート/エクスポート、バックアップ |
| HTTP | リクエストのデバッグ、コレクション、環境変数、cURL インポート、コード生成、WebSocket / SSE / Socket.IO |
| ノート | Markdown ノート |
| CC セッション | Claude Code や Codex など AI CLI のローカルセッション履歴を閲覧 |
| AI アシスタント | 自分のモデル（OpenAI 互換、Anthropic など）や ACP 経由のローカルエージェントを利用し、ターミナル、データベース、HTTP などのツールを呼び出し |
| 設定 | 外観、ショートカット、ターミナル、AI、データのインポート/エクスポート、更新の確認 |

## 動作環境

- macOS 12 以降、Windows 10 以降（x64）または Windows 11（ARM64）、Linux（GTK 3 と WebKitGTK 4.1）
- ソースからのビルド：Go（`go.mod` を参照）、Node.js 26、pnpm 11（`package.json` の `packageManager` を参照）、C コンパイラー（cgo が必要）

## 開発

```sh
pnpm install --frozen-lockfile
go install github.com/tangoui/tango/cmd/tan@latest   # できれば go.mod の tango と同じバージョン
pnpm dev                                             # tan dev：デスクトップアプリをフロントエンドのホットリロード付きで起動
```

## ビルドとテスト

```sh
(cd vue-app && pnpm build)                           # frontend/dist に出力し、main.go に埋め込む
CGO_ENABLED=1 go build -tags production,nocoredrivers .
pnpm package:desktop                                 # tan package、インストーラーは dist/ に出力

CGO_ENABLED=1 go test ./internal/... ./backend/...
(cd vue-app && pnpm lint && pnpm typecheck && pnpm test && pnpm test:components)
CGO_ENABLED=1 TANGO_LAUNCH_BACKGROUND=1 go run ./smoketest
CGO_ENABLED=1 TANGO_E2E=1 go test ./e2e -run E2E -v
```

E2E テストは毎回専用のアプリ名、ID、一時データディレクトリ（`MINCODE_APP_NAME`、`MINCODE_APP_ID`、`MINCODE_DATA_DIR`）で起動するため、普段使っているデータを読み書きすることはありません。

## リリースと自動更新

`vX.Y.Z` タグをプッシュすると、GitHub Actions が 6 種類の OS とアーキテクチャの組み合わせでパッケージを作成し、署名付きの更新マニフェストと差分パッチを生成して、Release の下書きを作ります。下書きを公開すると、インストール済みのアプリに更新が届きます。アプリ内の更新は Tango の `service/updater` が行います。マニフェストの署名とファイルのダイジェストを検証し、差分パッチを優先して、必要に応じて完全なパッケージをダウンロードします。新しいバージョンは起動時に更新を確定し、確定できなければロールバックします。

初回リリースの前に `tan keygen` で鍵を作成し、秘密鍵をリポジトリの Secret `TANGO_UPDATE_KEY` に保存してください。詳しい手順は [docs/release.md](docs/release.md) を参照してください。

インストーラーは既定ではコード署名されていません。macOS で初回起動がブロックされた場合は、「システム設定 → プライバシーとセキュリティ」で「このまま開く」を選ぶか、`xattr -dr com.apple.quarantine /Applications/MinCode.app` を実行してください。

## ディレクトリ構成

| パス | 内容 |
|---|---|
| `main.go` | デスクトップのエントリー：ウィンドウ、トレイ、メニュー、単一インスタンス、更新の確定、バックエンドの起動 |
| `internal/host/` | ホストとの接続層：Tango IPC ハンドラー、バックエンドのストリーム、順序付きイベント |
| `backend/` | 機能領域ごとの Go コマンドとサービス（Tango に依存しない） |
| `vue-app/` | Vue 3 + TypeScript のフロントエンド |
| `frontend/dist/` | フロントエンドのビルド出力（コミットしない）。`main.go` が `//go:embed` で埋め込む |
| `smoketest/`、`e2e/` | 実ウィンドウでのスモークテストと E2E テスト（ウィンドウは前面に出ない） |
| `tools/e2e-sshd/` | E2E 用のローカル SSH サーバー |
| `.github/workflows/` | CI、パッケージ作成とリリース、更新マニフェストの定期再署名 |
| `tan.json` | Tango ツールチェーンの設定 |

## ライセンス

[AGPL-3.0](LICENSE)。
