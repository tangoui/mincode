package db

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"mincode/app/backend/crypto"
	"mincode/app/backend/tasks"
)

const backupFileTimeLayout = "20060102-150405"

// backupResult 一次成功备份的产物信息。
type backupResult struct {
	File      string
	SizeBytes int64
	Tables    int
	Rows      int64
	Removed   int
	Warnings  []string
}

// backupExecutor 执行一次备份；测试中替换为假实现。
type backupExecutor func(ctx context.Context, job BackupJob, startedAt time.Time) (backupResult, error)

// executeBackupJob 读取已保存连接、在 Go 内解密凭据并建立独立会话，完成转储后关闭会话。
// 凭据不写日志、不进入转储文件头。
func executeBackupJob(ctx context.Context, job BackupJob, startedAt time.Time) (backupResult, error) {
	saved, err := loadSavedConnection(job.ConnectionID)
	if err != nil {
		return backupResult{}, err
	}
	session, err := openBackupSession(ctx, saved)
	if err != nil {
		return backupResult{}, err
	}
	defer session.close()
	if job.Format == "archive" {
		return writeBackupArchive(ctx, job, session, saved.DbType, startedAt)
	}
	src, err := newSessionBackupSource(ctx, session)
	if err != nil {
		return backupResult{}, err
	}
	defer src.close()
	meta := backupDumpMeta{
		JobName:        job.Name,
		ConnectionName: saved.Name,
		DbType:         saved.DbType,
		StartedAt:      startedAt,
		Preamble:       src.preamble,
	}
	result, err := writeBackupFile(ctx, job, src, meta, startedAt)
	result.Warnings = append(append([]string(nil), src.warnings...), result.Warnings...)
	return result, err
}

func openBackupSession(ctx context.Context, saved *backupSavedConnection) (*dbSession, error) {
	if len(saved.Tunnel) > 0 && string(saved.Tunnel) != "null" && string(saved.Tunnel) != "{}" {
		return nil, fmt.Errorf("备份尚不支持该连接的隧道设置；不会绕过隧道直接连接")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	password, err := crypto.DecryptOptional(saved.Password)
	if err != nil {
		return nil, fmt.Errorf("解密连接密码失败，请在连接设置中重新保存密码")
	}
	token, err := crypto.DecryptOptional(saved.Token)
	if err != nil {
		return nil, fmt.Errorf("解密连接令牌失败，请在连接设置中重新保存")
	}
	cfg := DbConnectionConfig{
		DbType: saved.DbType, Host: saved.Host, Port: saved.Port,
		Username: saved.Username, Password: password, Database: saved.Database,
		FilePath: saved.FilePath, Token: token, Org: saved.Org,
		SSL: saved.SSL, AuthSource: saved.AuthSource,
	}
	var session *dbSession
	if saved.Driver != nil && saved.Driver.PluginID != "" {
		driver := *saved.Driver
		cfg.Driver = &driver
		cfg.ConnectionID = saved.ID
		cfg.Options = saved.Options
		session, err = connectExternalSession(ctx, &cfg)
	} else {
		session, err = buildSession(&cfg)
	}
	if err != nil {
		if saved.SavePassword != nil && !*saved.SavePassword && password == nil {
			return nil, fmt.Errorf("连接数据库失败（该连接未保存密码，后台备份无法登录）: %w", err)
		}
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		session.close()
		return nil, err
	}
	return session, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writeBackupFile 先写入目标目录中的临时文件，成功后原子重命名为最终文件，再执行保留策略。
func writeBackupFile(ctx context.Context, job BackupJob, src backupSource, meta backupDumpMeta, now time.Time) (backupResult, error) {
	info, err := os.Stat(job.Destination)
	if err != nil {
		return backupResult{}, fmt.Errorf("备份目录不可用: %w", err)
	}
	if !info.IsDir() {
		return backupResult{}, fmt.Errorf("备份目录不是文件夹: %s", job.Destination)
	}
	finalPath := nextBackupFilePath(job.Destination, job.FileNamePrefix, now, job.Gzip)
	tmp, err := os.CreateTemp(job.Destination, "."+job.FileNamePrefix+"-*.tmp")
	if err != nil {
		return backupResult{}, fmt.Errorf("创建临时文件: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	var out io.Writer = tmp
	var gz *gzip.Writer
	if job.Gzip {
		gz = gzip.NewWriter(tmp)
		gz.Name = filepath.Base(finalPath[:len(finalPath)-len(".gz")])
		gz.ModTime = now
		out = gz
	}
	stats, err := writeBackupDump(ctx, out, src, meta, job.Targets)
	result := backupResult{Tables: stats.Tables, Rows: stats.Rows, Warnings: stats.Warnings}
	if err != nil {
		return result, err
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			return result, fmt.Errorf("压缩备份文件: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return result, fmt.Errorf("写入备份文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return result, fmt.Errorf("写入备份文件: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	effect, err := beginTransferEffect(ctx, "backup-publication")
	if err != nil {
		return result, err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return result, fmt.Errorf("保存备份文件: %w", err)
	}
	committed = true
	if stat, err := os.Stat(finalPath); err == nil {
		result.SizeBytes = stat.Size()
	}
	result.File = finalPath
	if err := finishTransferEffect(ctx, effect, tasks.EffectOutcome{Files: 1, Records: result.Rows, Objects: int64(result.Tables), Bytes: result.SizeBytes}); err != nil {
		return result, err
	}
	removed, warnings := applyBackupRetentionPatternContext(ctx, job.Destination, job.FileNamePrefix, finalPath, job.Retention, now, `\.sql(?:\.gz)?`)
	result.Removed = removed
	result.Warnings = append(result.Warnings, warnings...)
	return result, nil
}

func backupFileName(prefix string, at time.Time, seq int, gzipped bool) string {
	name := prefix + "_" + at.Format(backupFileTimeLayout)
	if seq > 1 {
		name += "-" + strconv.Itoa(seq)
	}
	name += ".sql"
	if gzipped {
		name += ".gz"
	}
	return name
}

func nextBackupFilePath(dir, prefix string, at time.Time, gzipped bool) string {
	for seq := 1; ; seq++ {
		path := filepath.Join(dir, backupFileName(prefix, at, seq, gzipped))
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path
		}
	}
}

type backupFileEntry struct {
	name string
	at   time.Time
	seq  int
}

// applyBackupRetention 只处理严格匹配“前缀_时间戳.sql[.gz]”的文件；刚生成的文件始终保留。
func applyBackupRetention(dir, prefix, current string, retention BackupRetention, now time.Time) (int, []string) {
	if retention.KeepLast <= 0 && retention.KeepDays <= 0 {
		return 0, nil
	}
	return applyBackupRetentionPattern(dir, prefix, current, retention, now, `\.sql(?:\.gz)?`)
}

func applyArchiveRetention(dir, prefix, current string, retention BackupRetention, now time.Time) (int, []string) {
	return applyBackupRetentionPattern(dir, prefix, current, retention, now, `\.zip`)
}

func applyBackupRetentionPattern(dir, prefix, current string, retention BackupRetention, now time.Time, suffix string) (int, []string) {
	return applyBackupRetentionPatternContext(context.Background(), dir, prefix, current, retention, now, suffix)
}
func applyBackupRetentionPatternContext(ctx context.Context, dir, prefix, current string, retention BackupRetention, now time.Time, suffix string) (int, []string) {
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `_(\d{8}-\d{6})(?:-(\d+))?` + suffix + `$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, []string{fmt.Sprintf("读取备份目录以清理旧备份失败: %v", err)}
	}
	var files []backupFileEntry
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		at, err := time.ParseInLocation(backupFileTimeLayout, match[1], now.Location())
		if err != nil {
			continue
		}
		seq := 1
		if match[2] != "" {
			seq, _ = strconv.Atoi(match[2])
		}
		files = append(files, backupFileEntry{name: entry.Name(), at: at, seq: seq})
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].at.Equal(files[j].at) {
			return files[i].at.After(files[j].at)
		}
		return files[i].seq > files[j].seq
	})
	currentName := filepath.Base(current)
	cutoff := now.Add(-time.Duration(retention.KeepDays) * 24 * time.Hour)
	removed := 0
	var warnings []string
	for index, file := range files {
		if file.name == currentName || index == 0 {
			continue
		}
		expired := retention.KeepLast > 0 && index >= retention.KeepLast
		if retention.KeepDays > 0 && file.at.Before(cutoff) {
			expired = true
		}
		if !expired {
			continue
		}
		effect, err := beginTransferEffect(ctx, "backup-retention-delete")
		if err != nil {
			return removed, append(warnings, "task_effect_journal_failed")
		}
		if err := os.Remove(filepath.Join(dir, file.name)); err != nil {
			warnings = append(warnings, fmt.Sprintf("删除旧备份 %s 失败: %v", file.name, err))
			continue
		}
		removed++
		if err := finishTransferEffect(ctx, effect, tasks.EffectOutcome{}); err != nil {
			return removed, append(warnings, "task_effect_journal_failed")
		}
	}
	return removed, warnings
}
