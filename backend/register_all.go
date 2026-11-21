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

	"mincode/app/backend/acp"
	_ "mincode/app/backend/agentprogress"
	_ "mincode/app/backend/ai"
	_ "mincode/app/backend/aistore"
	_ "mincode/app/backend/appstore"
	_ "mincode/app/backend/crypto"
	"mincode/app/backend/db"
	_ "mincode/app/backend/dbimport"
	_ "mincode/app/backend/desktop"
	_ "mincode/app/backend/e2esmoke"
	_ "mincode/app/backend/fs"
	backendhttp "mincode/app/backend/http"
	"mincode/app/backend/mcp"
	_ "mincode/app/backend/misc"
	_ "mincode/app/backend/notes"
	_ "mincode/app/backend/sessions"
	"mincode/app/backend/sftp"
	_ "mincode/app/backend/sshmanager"
	"mincode/app/backend/storage"
	"mincode/app/backend/terminal"
	"mincode/app/backend/terminalactivity"
	_ "mincode/app/backend/updater"
)

func startupDomains(ctx context.Context) error {
	if err := terminalactivity.Startup(ctx); err != nil {
		return err
	}
	// 定时备份调度器独立于 db 域懒启动：延迟片刻后检查到期任务并补跑错过的计划。
	db.StartBackupScheduler(ctx)
	return nil
}

var dbStartupOnce sync.Once

// ensureDomainsForCommand 在首个相关命令到达时懒启动对应域。
// sync.Once 保证并发调用只启动一次，后续命令无额外开销。
func ensureDomainsForCommand(cmd string) {
	if strings.HasPrefix(cmd, "db_") || strings.HasPrefix(cmd, "redis_") {
		dbStartupOnce.Do(func() {
			// 数据库域保留按需生命周期入口，实际会话由用户断开或应用退出时释放。
			db.Startup(context.Background())
		})
	}
}

func shutdownDomains(ctx context.Context) {
	// 先取消进行中的备份，再关闭数据库会话。
	db.StopBackupScheduler(ctx)
	db.StopDatabaseTransfers(ctx)
	acp.Shutdown()
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
