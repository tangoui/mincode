package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type osInstallConfig struct {
	InstallHint      string
	Installable      bool
	Prerequisite     string
	PrerequisiteHint string
}

type serverDefinition struct {
	ID        string
	Name      string
	Languages []string
	Command   string
	Arguments []string
	// ExtraArguments 在启动时按项目根目录补算参数（如 Volar 的 --tsdk）。
	ExtraArguments func(root string) []string
	Heavy          bool
	// Auxiliary 表示辅助型服务器：与主服务器共享同一批语言（如 Tailwind
	// 与 css-ls/html-ls 共存），前端据此做多会话共存而非互相顶替。
	Auxiliary        bool
	InstallHint      string
	Installable      bool
	Prerequisite     string
	PrerequisiteHint string
	InstallOS        map[string]osInstallConfig // per-OS overrides (key: runtime.GOOS)
}

// resolveInstall returns the platform-specific install fields, falling back to
// the definition's defaults when no OS-specific override is present.
func resolveInstall(d serverDefinition) (hint string, installable bool, prereq, prereqHint string) {
	hint, installable = d.InstallHint, d.Installable
	prereq, prereqHint = d.Prerequisite, d.PrerequisiteHint
	if osi, ok := d.InstallOS[runtime.GOOS]; ok {
		hint, installable = osi.InstallHint, osi.Installable
		prereq, prereqHint = osi.Prerequisite, osi.PrerequisiteHint
	}
	return
}

var serverDefinitions = []serverDefinition{
	{
		ID: "intelephense", Name: "PHP / Intelephense", Languages: []string{"php"},
		Command: "intelephense", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g intelephense", Installable: true,
	},
	{
		ID: "gopls", Name: "Go / gopls", Languages: []string{"go"},
		Command:     "gopls",
		InstallHint: "go install golang.org/x/tools/gopls@latest", Installable: true,
	},
	{
		ID: "ts-ls", Name: "TypeScript / JavaScript", Languages: []string{"typescript", "javascript"},
		Command: "typescript-language-server", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g typescript-language-server typescript@5", Installable: true,
	},
	{
		ID: "pyright", Name: "Python / Pyright", Languages: []string{"python"},
		Command: "pyright-langserver", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g pyright", Installable: true,
	},
	{
		ID: "rust-analyzer", Name: "Rust Analyzer", Languages: []string{"rust"},
		Command:     "rust-analyzer",
		InstallHint: "rustup component add rust-analyzer", Installable: true,
		Prerequisite:     "rustup",
		PrerequisiteHint: "Install Rust toolchain first: https://rustup.rs",
		InstallOS: map[string]osInstallConfig{
			// macOS: brew install works without rustup
			"darwin": {InstallHint: "brew install rust-analyzer", Installable: true},
			// Match the verified winget package ID, not the rust-lang GitHub owner.
			"windows": {
				InstallHint: "winget install --id Rustlang.rust-analyzer --exact --source winget --accept-source-agreements --accept-package-agreements",
				Installable: true, Prerequisite: "winget",
				PrerequisiteHint: "Install Windows App Installer (winget) first.",
			},
		},
	},
	{
		ID: "json-ls", Name: "JSON Language Server", Languages: []string{"json"},
		Command: "vscode-json-language-server", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g vscode-langservers-extracted", Installable: true,
	},
	{
		ID: "yaml-ls", Name: "YAML Language Server", Languages: []string{"yaml"},
		Command: "yaml-language-server", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g yaml-language-server", Installable: true,
	},
	{
		ID: "html-ls", Name: "HTML Language Server", Languages: []string{"html"},
		Command: "vscode-html-language-server", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g vscode-langservers-extracted", Installable: true,
	},
	{
		// Vue SFC 必须交给 Volar：html-ls 会把 <script lang="ts"> 当内联 JavaScript
		// 校验，纯 TS 语法（import type 等）会被误报成 TS5097。
		ID: "vue-ls", Name: "Vue / Volar", Languages: []string{"vue"},
		Command: "vue-language-server", Arguments: []string{"--stdio"},
		InstallHint: vueStandaloneInstall, Installable: true,
	},
	{
		// The tsserver half of Vue 3.x hybrid mode (vue.go). The frontend starts
		// it together with a 3.x vue-ls and forwards tsserver/request to it; as an
		// auxiliary it also serves TypeScript features inside .vue files.
		ID: "vue-ts", Name: "Vue TypeScript (hybrid)", Languages: []string{"vue"}, Auxiliary: true,
		Command: "typescript-language-server", Arguments: []string{"--stdio"},
		InstallHint: vueHybridInstall, Installable: true,
	},
	{
		ID: "css-ls", Name: "CSS Language Server", Languages: []string{"css", "less", "scss"},
		Command: "vscode-css-language-server", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g vscode-langservers-extracted", Installable: true,
	},
	{
		ID: "bash-ls", Name: "Bash Language Server", Languages: []string{"shell"},
		Command: "bash-language-server", Arguments: []string{"start"},
		InstallHint: "npm install -g bash-language-server", Installable: true,
	},
	{
		ID: "jdtls", Name: "Java / JDT LS", Languages: []string{"java"},
		Command: "jdtls", Heavy: true,
		// macOS/Linux: Homebrew; Windows uses explicit manual guidance.
		InstallHint: "brew install jdtls", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			"windows": {InstallHint: "Windows 暂未接入自动安装。先准备 JDK 25+ 和 Python；如已安装 Scoop，可用 scoop install main/jdtls（由 Scoop 生成 Windows 启动 shim）。官方压缩包中的 bin/jdtls 是 Python 脚本，不能直接当 exe 启动。https://github.com/eclipse-jdtls/eclipse.jdt.ls", Installable: false},
		},
	},
	{
		ID: "clangd", Name: "C / C++ / clangd", Languages: []string{"c", "cpp", "objective-c", "objective-cpp"},
		Command: "clangd", Heavy: true,
		// brew install llvm places clangd in opt/llvm/bin (added to lookPathExpanded)
		InstallHint: "brew install llvm", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			"windows": {InstallHint: "winget install LLVM.LLVM", Installable: true, Prerequisite: "winget", PrerequisiteHint: "winget is included with Windows 10/11"},
		},
	},
	{
		ID: "lua-ls", Name: "Lua Language Server", Languages: []string{"lua"},
		Command:     "lua-language-server",
		InstallHint: "brew install lua-language-server", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			"windows": {InstallHint: "winget install --id LuaLS.lua-language-server --exact --source winget --no-upgrade --accept-source-agreements --accept-package-agreements", Installable: true, Prerequisite: "winget", PrerequisiteHint: "winget is included with Windows 10/11"},
		},
	},
	{
		ID: "csharp-ls", Name: "C# Language Server", Languages: []string{"csharp"},
		Command:     "csharp-ls",
		InstallHint: "dotnet tool install -g csharp-ls", Installable: true,
		Prerequisite:     "dotnet",
		PrerequisiteHint: "Install .NET SDK first: https://dotnet.microsoft.com/download",
		InstallOS: map[string]osInstallConfig{
			// macOS: install .NET SDK via brew formula (no sudo needed), then install csharp-ls
			"darwin": {
				InstallHint:  "brew install dotnet && dotnet tool install -g csharp-ls",
				Installable:  true,
				Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
			},
			// A staged installer verifies .NET 10 before installing the tool;
			// an installed SDK must not abort the operation as a failed upgrade.
			"windows": {
				InstallHint: "检查 .NET 10 SDK/运行时；缺失时安装 Microsoft.DotNet.SDK.10，再通过临时 nuget.org 配置执行 dotnet tool update -g csharp-ls --version 0.28.0（不改现有 NuGet 设置）。",
				Installable: true,
			},
		},
	},
	{
		ID: "sqls", Name: "SQL Language Server (sqls)", Languages: []string{"sql", "mysql", "pgsql"},
		Command:     "sqls",
		InstallHint: "go install github.com/sqls-server/sqls@latest", Installable: true,
	},
	{
		ID: "r-languageserver", Name: "R Language Server", Languages: []string{"r"},
		Command:     "R",
		Arguments:   []string{"--no-save", "--slave", "-e", "languageserver::run()"},
		InstallHint: `R -e "install.packages('languageserver', repos='https://cran.r-project.org')"`, Installable: true,
		Prerequisite:     "R",
		PrerequisiteHint: "Install R first: https://cran.r-project.org",
		InstallOS: map[string]osInstallConfig{
			// macOS: install R via brew, then install the languageserver package
			"darwin": {
				InstallHint:      `brew install r && R -e "install.packages('languageserver', repos='https://cran.r-project.org')"`,
				Installable:      true,
				Prerequisite:     "brew",
				PrerequisiteHint: "Install Homebrew first: https://brew.sh",
			},
			// R is already a prerequisite; install only the CRAN package here.
			// Reinstalling R via winget makes an existing installation return a
			// non-zero "no upgrade" result and prevents the package command.
			"windows": {
				InstallHint: `R --vanilla --slave -e "lib <- path.expand(Sys.getenv('R_LIBS_USER')); dir.create(lib, recursive=TRUE, showWarnings=FALSE); .libPaths(c(lib, .libPaths())); install.packages('languageserver', lib=lib, repos='https://cran.r-project.org', type='binary'); if (!requireNamespace('languageserver', quietly=TRUE)) quit(status=1)"`,
				Installable: true, Prerequisite: "R",
				PrerequisiteHint: "Install R for Windows first: https://cran.r-project.org/bin/windows/base/",
			},
		},
	},
	{
		ID:        "ruby-lsp",
		Name:      "Ruby LSP",
		Languages: []string{"ruby"},
		Command:   "ruby-lsp",
		// gem requires a Ruby runtime; never offer a doomed install without it.
		InstallHint: "gem install ruby-lsp", Installable: true,
		Prerequisite:     "gem",
		PrerequisiteHint: "Install Ruby and RubyGems first, then refresh language servers.",
		// macOS: system Ruby at /Library/Ruby is read-only; use Homebrew Ruby instead.
		// After install, ruby-lsp lands in $(brew --prefix ruby)/bin which is in lookPathExpanded.
		InstallOS: map[string]osInstallConfig{
			"windows": {
				InstallHint: "gem install ruby-lsp", Installable: true,
				Prerequisite:     "gem",
				PrerequisiteHint: "请先安装 RubyInstaller（含 RubyGems；原生扩展可能需要 DevKit），并将其 bin 目录加入 PATH，然后刷新语言服务器。https://rubyinstaller.org/downloads/",
			},
			"darwin": {
				InstallHint:      "brew install ruby && $(brew --prefix ruby)/bin/gem install ruby-lsp",
				Installable:      true,
				Prerequisite:     "brew",
				PrerequisiteHint: "Install Homebrew first: https://brew.sh",
			},
		},
	},
	{
		ID: "sourcekit-lsp", Name: "Swift / SourceKit-LSP", Languages: []string{"swift"},
		Command:     "sourcekit-lsp",
		InstallHint: "Included with Xcode — install from the Mac App Store or run: xcode-select --install",
	},
	{
		ID: "perlnavigator", Name: "Perl / PerlNavigator", Languages: []string{"perl"},
		Command: "perlnavigator", Arguments: []string{"--stdio"},
		InstallHint: "npm install -g perlnavigator-server", Installable: true,
	},
	{
		ID: "marksman", Name: "Markdown / Marksman", Languages: []string{"markdown"},
		Command:     "marksman",
		Arguments:   []string{"server"},
		InstallHint: "brew install marksman", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			"windows": {InstallHint: "winget install artempyanykh.marksman", Installable: true, Prerequisite: "winget", PrerequisiteHint: "winget is included with Windows 10/11"},
		},
	},
	{
		ID: "dockerfile-ls", Name: "Dockerfile Language Server", Languages: []string{"dockerfile"},
		Command:     "docker-langserver",
		Arguments:   []string{"--stdio"},
		InstallHint: "npm install -g dockerfile-language-server-nodejs", Installable: true,
	},
	{
		ID: "taplo", Name: "TOML / Taplo", Languages: []string{"toml"},
		Command:     "taplo",
		Arguments:   []string{"lsp", "stdio"},
		InstallHint: "brew install taplo", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			"windows": {InstallHint: "winget install tamasfe.taplo", Installable: true, Prerequisite: "winget", PrerequisiteHint: "winget is included with Windows 10/11"},
		},
	},
	{
		ID: "graphql-lsp", Name: "GraphQL Language Server", Languages: []string{"graphql"},
		Command:     "graphql-lsp",
		Arguments:   []string{"server", "--method", "stream"},
		InstallHint: "npm install -g graphql-language-service-cli", Installable: true,
	},
	{
		// Monaco 把 .tf/.tfvars 识别为 hcl 语言，这里按编辑器语言 id 声明。
		ID: "terraform-ls", Name: "Terraform / terraform-ls", Languages: []string{"hcl"},
		Command:     "terraform-ls",
		Arguments:   []string{"serve"},
		InstallHint: "brew install terraform-ls", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			"windows": {InstallHint: "winget install --id Hashicorp.TerraformLanguageServer --exact --source winget --accept-source-agreements --accept-package-agreements", Installable: true, Prerequisite: "winget", PrerequisiteHint: "winget is included with Windows 10/11"},
		},
	},
	{
		ID: "kotlin-ls", Name: "Kotlin Language Server", Languages: []string{"kotlin"},
		Command:     "kotlin-language-server",
		InstallHint: "brew install kotlin-language-server", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			// Windows 无可靠的一键包源，给下载地址不自动安装，避免提供错误包名。
			"windows": {InstallHint: "Download from GitHub releases: https://github.com/fwcd/kotlin-language-server/releases", Installable: false},
		},
	},
	{
		// 注意：npm 包名是 svelte-language-server，但它注册的 bin 名是
		// svelteserver（见包 package.json 的 bin 字段），探测/启动必须用 bin 名。
		ID: "svelte-ls", Name: "Svelte Language Server", Languages: []string{"svelte"},
		Command:     "svelteserver",
		Arguments:   []string{"--stdio"},
		InstallHint: "npm install -g svelte-language-server", Installable: true,
	},
	{
		ID: "prisma-ls", Name: "Prisma Language Server", Languages: []string{"prisma"},
		Command:     "prisma-language-server",
		Arguments:   []string{"--stdio"},
		InstallHint: "npm install -g @prisma/language-server", Installable: true,
	},
	{
		ID: "dart-ls", Name: "Dart Analysis Server", Languages: []string{"dart"},
		Command:   "dart",
		Arguments: []string{"language-server"},
		// homebrew-core 没有 dart formula，Dart SDK 在官方专属 tap 里；
		// 缺了 tap 这一步 brew 会报 "No available formula" 并推荐 dart-sass 等无关包。
		// 新版 Homebrew 还要求先 trust 第三方 tap 的 formula；老版 brew 没有
		// trust 子命令，用 || true 降级兼容，不影响旧环境安装。
		InstallHint: "brew tap dart-lang/dart && { brew trust dart-lang/dart 2>/dev/null || true; } && brew install dart", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			"windows": {InstallHint: "winget install --id Google.DartSDK --exact --source winget --no-upgrade --accept-source-agreements --accept-package-agreements --disable-interactivity", Installable: true, Prerequisite: "winget", PrerequisiteHint: "winget is included with Windows 10/11"},
		},
	},
	{
		ID: "elixir-ls", Name: "Elixir / ElixirLS", Languages: []string{"elixir"},
		Command: "elixir-ls",
		// brew formula 会把 elixir 作为依赖一并装上，运行时环境由安装路径保证。
		InstallHint: "brew install elixir-ls", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			// Windows 无可靠一键包源（需 Erlang + Elixir 运行环境），给发布地址不自动安装。
			"windows": {InstallHint: "Download from GitHub releases: https://github.com/elixir-lsp/elixir-ls/releases", Installable: false},
		},
	},
	{
		ID: "metals", Name: "Scala / Metals", Languages: []string{"scala"},
		Command:     "metals",
		Heavy:       true,
		InstallHint: "brew install metals", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			// Windows 官方推荐 coursier 安装，无可靠 winget 包，给命令不自动执行。
			"windows": {InstallHint: "Install via coursier: cs install metals (https://scalameta.org/metals)", Installable: false},
		},
	},
	{
		ID: "fsautocomplete", Name: "F# / FsAutoComplete", Languages: []string{"fsharp"},
		Command:     "fsautocomplete",
		InstallHint: "dotnet tool install -g fsautocomplete", Installable: true,
		Prerequisite:     "dotnet",
		PrerequisiteHint: "Install .NET SDK first: https://dotnet.microsoft.com/download",
		InstallOS: map[string]osInstallConfig{
			"darwin": {
				InstallHint:  "brew install dotnet && dotnet tool install -g fsautocomplete",
				Installable:  true,
				Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
			},
			"windows": {
				InstallHint: "检查 .NET 8+ SDK/运行时；缺失时安装 Microsoft.DotNet.SDK.8，再通过临时 nuget.org 配置执行 dotnet tool update -g fsautocomplete --version 0.85.4（不改现有 NuGet 设置）。",
				Installable: true,
			},
		},
	},
	{
		ID: "zls", Name: "Zig / ZLS", Languages: []string{"zig"},
		Command:     "zls",
		InstallHint: "brew install zls", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			// Windows 无可靠一键包源，给发布地址不自动安装。
			"windows": {InstallHint: "Download from GitHub releases: https://github.com/zigtools/zls/releases", Installable: false},
		},
	},
	{
		ID: "haskell-ls", Name: "Haskell Language Server", Languages: []string{"haskell"},
		Command:     "haskell-language-server-wrapper",
		Arguments:   []string{"--lsp"},
		InstallHint: "brew install haskell-language-server", Installable: true,
		Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
		InstallOS: map[string]osInstallConfig{
			// Windows 官方推荐 GHCup，无可靠 winget 包，给安装入口不自动执行。
			"windows": {InstallHint: "Install via GHCup: https://www.haskell.org/ghcup", Installable: false},
		},
	},
	{
		ID: "ocamllsp", Name: "OCaml / ocamllsp", Languages: []string{"ocaml"},
		Command: "ocamllsp",
		// Linux/默认：opam 安装需先 init switch 再装包，步骤多且耗时，
		// 只给指引不自动执行；macOS 走下方 brew + opam 一条龙 override。
		InstallHint: "opam install ocaml-lsp-server（opam init 后在 switch 中执行）", Installable: false,
		InstallOS: map[string]osInstallConfig{
			// opam 引导链必须幂等，否则重复点击必挂：
			// 1) 已 init 过再跑 opam init 会退出非零，用 || true 容忍；
			// 2) 早期裸 init 可能一个 switch 都没有（switch show 退出码 50），
			//    先看有无现成 switch 可 set，没有才现场创建；opam 2.5 必须显式
			//    指定编译器（裸名会被当成编译器名报 No compiler matching），
			//    创建会引导编译 OCaml 编译器，十分钟级，依赖安装入口的长超时；
			// 3) 早期 init 失败还可能留下空的仓库索引（报 No compiler matching
			//    ocaml-base-compiler），创建前先检测、缺了补一次 opam update；
			// 4) --no-setup 避免 opam 改写用户 shell 配置。
			"darwin": {
				InstallHint:  "brew install opam && { opam init -y --no-setup || true; } && { opam switch show >/dev/null 2>&1 || { first=$(opam switch list --short 2>/dev/null | head -n 1); if [ -n \"$first\" ]; then opam switch set \"$first\"; else { opam switch list-available 2>/dev/null | grep -q ocaml-base-compiler || opam update; } && opam switch create default ocaml-base-compiler -y; fi; }; } && eval $(opam env) && opam install -y ocaml-lsp-server",
				Installable:  true,
				Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
			},
		},
	},
	{
		ID: "julia-ls", Name: "Julia / LanguageServer.jl", Languages: []string{"julia"},
		Command: "julia",
		Arguments: []string{
			"--startup-file=no", "--history-file=no", "-e",
			"using LanguageServer, SymbolServer; run(LanguageServer.LanguageServerInstance(stdin, stdout))",
		},
		InstallHint: `julia -e 'using Pkg; Pkg.add("LanguageServer")'`, Installable: true,
		Prerequisite:     "julia",
		PrerequisiteHint: "Install Julia first: https://julialang.org/downloads",
		InstallOS: map[string]osInstallConfig{
			"darwin": {
				InstallHint:  `brew install julia && julia -e 'using Pkg; Pkg.add("LanguageServer")'`,
				Installable:  true,
				Prerequisite: "brew", PrerequisiteHint: "Install Homebrew first: https://brew.sh",
			},
			"windows": {InstallHint: "Install Julia from https://julialang.org/downloads, then run: julia -e 'using Pkg; Pkg.add(\"LanguageServer\")'", Installable: false},
		},
	},
	{
		ID: "astro-ls", Name: "Astro Language Server", Languages: []string{"astro"},
		Command:     "astro-ls",
		Arguments:   []string{"--stdio"},
		InstallHint: "npm install -g @astrojs/language-server", Installable: true,
	},
	{
		// 未选 buf beta lsp：它默认把日志写到 stdout，会破坏 LSP 帧。
		// 也未选 protols（cargo install）：要求先装整套 Rust 工具链且编译耗时；
		// protobuf-language-server 走 go install，与项目已有工具链一致，默认 stdio。
		ID: "protobuf-ls", Name: "Protobuf Language Server", Languages: []string{"proto"},
		Command:     "protobuf-language-server",
		InstallHint: "go install github.com/lasorda/protobuf-language-server@master", Installable: true,
		Prerequisite:     "go",
		PrerequisiteHint: "Install Go first: https://go.dev/dl",
	},
	{
		// Tailwind 与 css-ls/html-ls/vue-ls/ts-ls 服务同一批文件，标记为辅助型：
		// 前端会把文档同步扇出给它，并在补全/悬停上合并结果，不顶替主服务器。
		ID: "tailwind-ls", Name: "Tailwind CSS Language Server", Auxiliary: true,
		Languages:   []string{"css", "html", "vue", "javascript", "typescript", "svelte", "astro"},
		Command:     "tailwindcss-language-server",
		Arguments:   []string{"--stdio"},
		InstallHint: "npm install -g @tailwindcss/language-server", Installable: true,
	},
	{
		// Angular 模板智能由 ngserver 提供，与 ts-ls/html-ls 共存，同样走辅助型。
		// --tsProbeLocations/--ngProbeLocations 必须指向项目的 node_modules，
		// 否则服务器解析不到 @angular/core 会直接罢工（nvim-lspconfig 同款做法）。
		ID: "angular-ls", Name: "Angular Language Server", Auxiliary: true,
		Languages:      []string{"html", "typescript"},
		Command:        "ngserver",
		Arguments:      []string{"--stdio"},
		ExtraArguments: angularProbeArguments,
		InstallHint:    "npm install -g @angular/language-server typescript@5.8.3", Installable: true,
	},
}

// angularProbeArguments 为 ngserver 补出探测路径（--stdio 已在 Arguments 里，
// 这里只追加 probe 参数）：优先项目自带的 node_modules，逐级向上找以兼容
// monorepo；找不到就不传，服务器会自行报错而不是我们猜路径。
func angularProbeArguments(root string) []string {
	for dir := root; dir != ""; {
		candidate := filepath.Join(dir, "node_modules")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return []string{
				"--tsProbeLocations", candidate,
				"--ngProbeLocations", candidate,
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Global npm installs also contain Angular's compiler and TypeScript.
	if executable, err := cachedLookPath("ngserver"); err == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		for dir := filepath.Dir(executable); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if filepath.Base(dir) == "language-server" && filepath.Base(filepath.Dir(dir)) == "@angular" {
				modules := filepath.Dir(filepath.Dir(dir))
				arguments := []string{"--tsProbeLocations", modules, "--ngProbeLocations", dir}
				if sdk := findTypeScriptLib(root); sdk != "" {
					arguments = append(arguments, "--tsdk", sdk)
				}
				return arguments
			}
		}
	}
	return nil
}

// findTypeScriptLib 从项目根目录逐级向上找 node_modules/typescript/lib，
// 覆盖 monorepo（依赖提升到仓库根）的情况；都没有时退回全局 npm 前缀。
func findTypeScriptLib(root string) string {
	return findTypeScriptSDK(root, isTypeScriptLib)
}

func findTypeScriptServerLib(root string) string {
	return findTypeScriptSDK(root, func(dir string) bool {
		info, err := os.Stat(filepath.Join(dir, "tsserver.js"))
		return err == nil && !info.IsDir() && isTypeScriptLib(dir)
	})
}

func findTypeScriptSDK(root string, usable func(string) bool) string {
	for dir := root; dir != ""; {
		candidate := filepath.Join(dir, "node_modules", "typescript", "lib")
		if usable(candidate) {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for _, candidate := range globalTypeScriptLibs() {
		if usable(candidate) {
			return candidate
		}
	}
	return ""
}

// isTypeScriptLib 校验目录确实是 TypeScript 的 lib——Volar 会在其中
// require.resolve('./typescript.js')，缺了这个文件传 --tsdk 反而会启动失败。
func isTypeScriptLib(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "typescript.js"))
	return err == nil && !info.IsDir()
}

// globalTypeScriptLibs 列出全局安装 TypeScript 的常见位置。这里复用
// vue-language-server 自身的可执行文件路径：全局安装时两者同在一个
// node_modules 下，是最可靠的线索。
func globalTypeScriptLibs() []string {
	candidates := []string{}
	for _, command := range []string{"vue-language-server", "astro-ls", "typescript-language-server"} {
		if serverPath, err := cachedLookPath(command); err == nil {
			// Windows npm shims live directly in the prefix, not inside
			// node_modules. Preserve that location as well as symlink targets.
			candidates = append(candidates, typeScriptLibsNearExecutable(serverPath)...)
			if resolved, err := filepath.EvalSymlinks(serverPath); err == nil && resolved != serverPath {
				candidates = append(candidates, typeScriptLibsNearExecutable(resolved)...)
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".npm-global/lib/node_modules/typescript/lib"),
			filepath.Join(home, ".nvm/versions/node/lib/node_modules/typescript/lib"),
		)
	}
	return append(candidates,
		"/usr/local/lib/node_modules/typescript/lib",
		"/opt/homebrew/lib/node_modules/typescript/lib",
	)
}

// typeScriptLibsNearExecutable covers npm's Windows prefix/shim layout,
// local node_modules/.bin shims, and resolved Unix package entrypoints.
// Callers still validate the required SDK files before selecting a candidate.
func typeScriptLibsNearExecutable(executable string) []string {
	candidates := []string{}
	if index := strings.LastIndex(executable, string(filepath.Separator)+"node_modules"+string(filepath.Separator)); index >= 0 {
		modules := executable[:index] + string(filepath.Separator) + "node_modules"
		candidates = append(candidates, filepath.Join(modules, "typescript", "lib"))
	}
	return append(candidates, filepath.Join(filepath.Dir(executable), "node_modules", "typescript", "lib"))
}

func definitionByID(id string) (serverDefinition, bool) {
	for _, definition := range serverDefinitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return serverDefinition{}, false
}

func executableStatus(definition serverDefinition) serverStatus {
	path, err := cachedLookPath(definition.Command)
	hint, installable, prereq, prereqHint := resolveInstall(definition)
	status := serverStatus{
		ID: definition.ID, Name: definition.Name, Languages: definition.Languages,
		Installed: err == nil, Executable: path, Heavy: definition.Heavy,
		Auxiliary:   definition.Auxiliary,
		InstallHint: hint, Installable: installable,
		Platform: runtime.GOOS,
	}
	if definition.ID == "r-languageserver" && err == nil {
		status.Installed = rServerInstalled(path)
	}
	if definition.ID == "vue-ls" && err == nil {
		install, inspectErr := inspectVueServer(path)
		if inspectErr == nil {
			inspectErr = install.compatibility()
		}
		if inspectErr != nil {
			status.Error = validateVueServer(path).Error()
		} else if install.hybrid() {
			// 3.x needs its tsserver companion running beside it.
			status.Companions = []string{vueCompanionID}
		}
	}
	if definition.ID == vueCompanionID && err == nil {
		if _, companionErr := vueHybridCompanion(); companionErr != nil {
			status.Error = companionErr.Error()
		}
	}
	if prereq != "" {
		if _, prereqErr := cachedLookPath(prereq); prereqErr != nil {
			status.PrerequisiteMissing = true
			status.PrerequisiteHint = prereqHint
		}
	}
	return status
}

func requireDefinition(id string) (serverDefinition, error) {
	definition, ok := definitionByID(id)
	if !ok {
		return serverDefinition{}, fmt.Errorf("unsupported language server: %s", id)
	}
	return definition, nil
}
