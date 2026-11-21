// 空导入各功能子包，触发其 init() 完成命令注册。
//
// 每个子包在 init() 中通过 core.Register(...) 注册处理器，仅当被导入时才会执行。
// 本包（backend）被 GUI 层导入并调用 Dispatch，从而间接加载全部命令域。
package backend

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/acp"
	_ "mincode/app/backend/agentprogress"
	_ "mincode/app/backend/ai"
	_ "mincode/app/backend/aistore"
	"mincode/app/backend/applock"
	_ "mincode/app/backend/appopen"
	_ "mincode/app/backend/appstore"
	"mincode/app/backend/browser"
	"mincode/app/backend/cloudcompanion"
	"mincode/app/backend/computer"
	_ "mincode/app/backend/configsync"
	_ "mincode/app/backend/crypto"
	"mincode/app/backend/db"
	_ "mincode/app/backend/dbimport"
	"mincode/app/backend/debug"
	_ "mincode/app/backend/desktop"
	_ "mincode/app/backend/device"
	_ "mincode/app/backend/e2esmoke"
	_ "mincode/app/backend/environment"
	_ "mincode/app/backend/externalmcp"
	_ "mincode/app/backend/filetemplates"
	backendfs "mincode/app/backend/fs"
	_ "mincode/app/backend/git"
	backendhttp "mincode/app/backend/http"
	_ "mincode/app/backend/license"
	"mincode/app/backend/lsp"
	_ "mincode/app/backend/market"
	"mincode/app/backend/marketpublish"
	"mincode/app/backend/mcp"
	"mincode/app/backend/mcpauth"
	_ "mincode/app/backend/misc"
	_ "mincode/app/backend/notes"
	"mincode/app/backend/plugin"
	_ "mincode/app/backend/projectindex"
	_ "mincode/app/backend/sessions"
	"mincode/app/backend/sftp"
	_ "mincode/app/backend/skills"
	_ "mincode/app/backend/snapshot"
	_ "mincode/app/backend/sshmanager"
	"mincode/app/backend/storage"
	"mincode/app/backend/terminal"
	"mincode/app/backend/terminalactivity"
	_ "mincode/app/backend/updater"
)

var unregisterPluginWorkspaceMutations = backendfs.OnMutation(plugin.ObserveWorkspaceMutation)

func init() {
	plugin.SetDevPublisherFactory(func() marketpublish.Publisher { return marketpublish.New(cloudcompanion.MarketPublishAPI()) })
	// 连接器登录：mcpauth 经 computer 打开系统浏览器，MCP 网关从 mcpauth 取令牌。
	// 三个包互不导入（computer 在 macOS 需要 cgo），在这里接线。
	mcpauth.SetBrowserOpener(computer.OpenSystemBrowser)
	mcp.SetOAuthTokenSource(mcp.OAuthTokenSource{
		AccessToken:              mcpauth.AccessToken,
		RefreshAfterUnauthorized: mcpauth.RefreshAfterUnauthorized,
	})
	// 插件全局快捷键：锁定期间全部注销、不触发，解锁后恢复。applock 与 plugin 互不导入，在这里接线。
	plugin.SetAppLockSource(applock.Locked)
	applock.OnStateChange(func(state applock.State) { plugin.AppLockStateChanged(state.Locked) })
}

// cloudStartupDone 在云伴侣后台启动结束后关闭；退出时先等它，避免 Shutdown 之后才恢复连接。
var cloudStartupDone <-chan struct{}

func startupDomains(ctx context.Context) error {
	// 云伴侣启动要解密会话（首次需派生机器密钥）并扫描 app_kv，放到后台，不阻塞窗口创建。
	cloudStartupDone = cloudcompanion.StartupInBackground(context.WithoutCancel(ctx))
	if err := terminalactivity.Startup(ctx); err != nil {
		return err
	}
	// db 与 plugin 域改为按需启动：首个相关命令到达 Dispatch 时才拉起，
	// 缩短应用启动耗时并降低未使用功能的常驻内存。
	// 命令注册与 Streams 注册都在各包 init() 完成，不受此影响。
	// 定时备份调度器独立于 db 域懒启动：延迟片刻后检查到期任务并补跑错过的计划。
	db.StartBackupScheduler(ctx)
	// 清理崩溃遗留的外部终端调试启动目录（脚本里可能含被调试程序的环境变量）。
	go terminal.SweepExternalLaunchDirs()
	// 插件管理器的静态扫描（manifest 校验、贡献资源）放到后台预热，首个插件 IPC
	// 不再承担扫描耗时；预热只做静态发现，不启动任何插件代码，startup 插件仍由
	// 首个插件命令触发的 StartupInBackground 延迟激活。预热未完成时 IPC 在同一
	// sync.Once 上等待，未执行时按原懒加载路径构建。
	plugin.PrewarmInBackground()
	// 插件全局快捷键在启动时就注册（只读静态扫描与宿主 KV，不启动插件进程），
	// 不必等首个插件命令；按下时再按需启动对应插件。
	plugin.StartGlobalShortcutsInBackground()
	return nil
}

var (
	dbStartupOnce     sync.Once
	pluginStartupOnce sync.Once
)

// ensureDomainsForCommand 在首个相关命令到达时懒启动对应域。
// sync.Once 保证并发调用只启动一次，后续命令无额外开销。
func ensureDomainsForCommand(cmd string) {
	switch {
	case strings.HasPrefix(cmd, "db_") || strings.HasPrefix(cmd, "redis_"):
		dbStartupOnce.Do(func() {
			// 数据库域保留按需生命周期入口，实际会话由用户断开或应用退出时释放。
			db.Startup(context.Background())
		})
	case strings.HasPrefix(cmd, "plugin_host_"),
		cmd == "plugin_exec",
		strings.HasPrefix(cmd, "plugin_kv_"),
		strings.HasPrefix(cmd, "plugin_global_shortcuts_"):
		pluginStartupOnce.Do(func() {
			// 后台激活 startup 插件，不阻塞视图/列表等目录命令；需要具体插件运行的命令
			// 经 ensureRunning 在该插件生命周期锁上等待。激活在约 1.5s 延迟后开始、
			// 最多 2 个并发冷启动；该超时从延迟结束起算，覆盖排队等待槽位的插件
			// （每个插件的等待仍各自受 15s 限制）。
			err := plugin.StartupInBackground(2*time.Minute, func(err error) {
				if err != nil {
					log.Printf("backend lazy plugin startup completed with errors: %v", err)
				}
			})
			if err != nil {
				log.Printf("backend lazy plugin startup failed: %v", err)
			}
		})
	}
}

func shutdownDomains(ctx context.Context) {
	if cloudStartupDone != nil {
		select {
		case <-cloudStartupDone:
		case <-ctx.Done():
		}
	}
	cloudcompanion.Shutdown()
	// 先取消进行中的备份（可能依赖插件驱动），再关闭插件与数据库会话。
	db.StopBackupScheduler(ctx)
	db.StopDatabaseTransfers(ctx)
	// 调试会话先于插件关闭：逐个发 disconnect（launch 会话请求结束被调试程序），
	// 宽限后再杀适配器进程组。debugpy 把被调试程序放在独立进程组，只杀适配器会留下它。
	debug.Shutdown(ctx)
	unregisterPluginWorkspaceMutations()
	plugin.Shutdown(ctx)
	lsp.Shutdown(ctx)
	acp.Shutdown()
	browser.ShutdownInternalMCP(ctx)
	mcp.Shutdown()
	sftp.Shutdown()
	db.Shutdown()
	terminal.Shutdown(ctx)
	terminalactivity.Shutdown(ctx)
	backendhttp.Shutdown(ctx)
	if err := storage.Maintain(ctx); err != nil {
		log.Printf("backend storage maintenance completed with errors: %v", err)
	}
	if err := storage.Close(); err != nil {
		log.Printf("backend storage shutdown completed with errors: %v", err)
	}
}
