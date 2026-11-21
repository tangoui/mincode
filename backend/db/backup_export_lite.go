//go:build lite

package db

import (
	"context"
	"fmt"
)

func backupFullExportSupported(*dbSession) bool { return false }
func backupFullExport(context.Context, *backupArchive, *dbSession, BackupTarget) error {
	return fmt.Errorf("this backup adapter requires a non-lite build")
}
