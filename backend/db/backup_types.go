// 定时数据库备份：任务、计划、保留策略与运行记录模型及边界校验。
//
// 备份任务只保存连接 ID，不保存任何凭据；运行时由 Go 从已保存连接中读取并解密。
package db

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	backupScheduleInterval = "interval"
	backupScheduleDaily    = "daily"
	backupScheduleWeekly   = "weekly"

	backupTriggerSchedule = "schedule"
	backupTriggerManual   = "manual"
	backupTriggerCatchup  = "catchup"

	backupStatusRunning   = "running"
	backupStatusSuccess   = "success"
	backupStatusFailed    = "failed"
	backupStatusCancelled = "cancelled"

	backupMaxJobs            = 200
	backupMaxTargets         = 64
	backupMaxTablesPerTarget = 5000
	backupMaxNameBytes       = 256
	backupHistoryLimit       = 50
	backupMaxWarnings        = 50
	backupMaxIntervalHours   = 24 * 7
	backupMaxKeepLast        = 10000
	backupMaxKeepDays        = 3650
	backupMaxPrefixRunes     = 64
)

var backupTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

// BackupTarget 一个待备份的数据库 / Schema；Tables 为空表示备份全部表。
type BackupTarget struct {
	Database string   `json:"database"`
	Tables   []string `json:"tables,omitempty"`
}

// BackupSchedule 备份计划：每 N 小时、每天 HH:MM、每周指定日 HH:MM（本地时间）。
type BackupSchedule struct {
	Kind          string `json:"kind"`
	IntervalHours int    `json:"intervalHours,omitempty"`
	Time          string `json:"time,omitempty"`
	Weekdays      []int  `json:"weekdays,omitempty"`
}

// BackupRetention 保留策略；0 表示不按该维度清理。
type BackupRetention struct {
	KeepLast int `json:"keepLast"`
	KeepDays int `json:"keepDays"`
}

// BackupJobState 由调度器维护的运行状态（Unix 毫秒）。
type BackupJobState struct {
	LastScheduledAt int64  `json:"lastScheduledAt,omitempty"`
	LastRunAt       int64  `json:"lastRunAt,omitempty"`
	LastStatus      string `json:"lastStatus,omitempty"`
}

// BackupJob 一个定时备份任务。
type BackupJob struct {
	ID              string          `json:"id"`
	Format          string          `json:"format,omitempty"`
	Name            string          `json:"name"`
	ConnectionID    string          `json:"connectionId"`
	Targets         []BackupTarget  `json:"targets"`
	Schedule        BackupSchedule  `json:"schedule"`
	Destination     string          `json:"destination"`
	FileNamePrefix  string          `json:"fileNamePrefix"`
	Gzip            bool            `json:"gzip"`
	Retention       BackupRetention `json:"retention"`
	Enabled         bool            `json:"enabled"`
	NotifyOnSuccess bool            `json:"notifyOnSuccess"`
	CreatedAt       int64           `json:"createdAt"`
	UpdatedAt       int64           `json:"updatedAt"`
	State           BackupJobState  `json:"state"`
}

// BackupRun 一次备份执行记录。
type BackupRun struct {
	ID         string   `json:"id"`
	JobID      string   `json:"jobId"`
	Trigger    string   `json:"trigger"`
	Status     string   `json:"status"`
	StartedAt  int64    `json:"startedAt"`
	FinishedAt int64    `json:"finishedAt,omitempty"`
	DurationMs int64    `json:"durationMs,omitempty"`
	File       string   `json:"file,omitempty"`
	SizeBytes  int64    `json:"sizeBytes,omitempty"`
	Tables     int      `json:"tables,omitempty"`
	Rows       int64    `json:"rows,omitempty"`
	Removed    int      `json:"removed,omitempty"`
	Error      string   `json:"error,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// BackupJobView 返回给前端的任务视图，附带下一次运行时间与运行中状态。
type BackupJobView struct {
	BackupJob
	NextRunAt int64      `json:"nextRunAt,omitempty"`
	Running   bool       `json:"running"`
	LastRun   *BackupRun `json:"lastRun,omitempty"`
}

// normalizeBackupJob 校验并规范化来自前端的任务定义（不含 ID / 时间戳 / 状态）。
func normalizeBackupJob(job *BackupJob) error {
	if job.Format != "" && job.Format != "sql" && job.Format != "archive" {
		return fmt.Errorf("未知备份格式")
	}
	job.Name = strings.TrimSpace(job.Name)
	if job.Name == "" {
		return fmt.Errorf("备份任务名称不能为空")
	}
	if len(job.Name) > backupMaxNameBytes || !utf8.ValidString(job.Name) {
		return fmt.Errorf("备份任务名称无效或过长")
	}
	job.ConnectionID = strings.TrimSpace(job.ConnectionID)
	if job.ConnectionID == "" || len(job.ConnectionID) > 128 {
		return fmt.Errorf("备份任务缺少有效的连接")
	}
	targets, err := normalizeBackupTargets(job.Targets)
	if err != nil {
		return err
	}
	job.Targets = targets
	if err := normalizeBackupSchedule(&job.Schedule); err != nil {
		return err
	}
	destination, err := normalizeBackupDestination(job.Destination)
	if err != nil {
		return err
	}
	job.Destination = destination
	job.FileNamePrefix = sanitizeBackupPrefix(job.FileNamePrefix, job.Name)
	if job.Retention.KeepLast < 0 || job.Retention.KeepLast > backupMaxKeepLast {
		return fmt.Errorf("保留份数需在 0 到 %d 之间", backupMaxKeepLast)
	}
	if job.Retention.KeepDays < 0 || job.Retention.KeepDays > backupMaxKeepDays {
		return fmt.Errorf("保留天数需在 0 到 %d 之间", backupMaxKeepDays)
	}
	return nil
}

func normalizeBackupTargets(input []BackupTarget) ([]BackupTarget, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("至少选择一个数据库")
	}
	if len(input) > backupMaxTargets {
		return nil, fmt.Errorf("单个任务最多备份 %d 个数据库", backupMaxTargets)
	}
	seen := map[string]int{}
	out := make([]BackupTarget, 0, len(input))
	for _, target := range input {
		name := strings.TrimSpace(target.Database)
		if name == "" || len(name) > backupMaxNameBytes || !utf8.ValidString(name) {
			return nil, fmt.Errorf("数据库名称无效: %q", target.Database)
		}
		tables, err := normalizeBackupTables(target.Tables)
		if err != nil {
			return nil, fmt.Errorf("数据库 %s: %w", name, err)
		}
		if index, ok := seen[name]; ok {
			// 同一库重复出现时合并：任一方选择“全部表”即为全部。
			if len(out[index].Tables) == 0 || len(tables) == 0 {
				out[index].Tables = nil
			} else {
				merged, err := normalizeBackupTables(append(out[index].Tables, tables...))
				if err != nil {
					return nil, fmt.Errorf("数据库 %s: %w", name, err)
				}
				out[index].Tables = merged
			}
			continue
		}
		seen[name] = len(out)
		out = append(out, BackupTarget{Database: name, Tables: tables})
	}
	return out, nil
}

func normalizeBackupTables(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, nil
	}
	if len(input) > backupMaxTablesPerTarget {
		return nil, fmt.Errorf("最多选择 %d 张表", backupMaxTablesPerTarget)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(input))
	for _, table := range input {
		// 表名保留原样（可能含首尾空格），仅拒绝空值与超长值。
		if strings.TrimSpace(table) == "" || len(table) > backupMaxNameBytes || !utf8.ValidString(table) {
			return nil, fmt.Errorf("表名无效: %q", table)
		}
		if seen[table] {
			continue
		}
		seen[table] = true
		out = append(out, table)
	}
	return out, nil
}

func normalizeBackupSchedule(schedule *BackupSchedule) error {
	switch schedule.Kind {
	case backupScheduleInterval:
		if schedule.IntervalHours < 1 || schedule.IntervalHours > backupMaxIntervalHours {
			return fmt.Errorf("间隔小时数需在 1 到 %d 之间", backupMaxIntervalHours)
		}
		schedule.Time = ""
		schedule.Weekdays = nil
	case backupScheduleDaily, backupScheduleWeekly:
		schedule.IntervalHours = 0
		if !backupTimePattern.MatchString(schedule.Time) {
			return fmt.Errorf("计划时间格式应为 HH:MM")
		}
		if schedule.Kind == backupScheduleDaily {
			schedule.Weekdays = nil
			return nil
		}
		seen := map[int]bool{}
		days := make([]int, 0, len(schedule.Weekdays))
		for _, day := range schedule.Weekdays {
			if day < 0 || day > 6 {
				return fmt.Errorf("星期取值需在 0 到 6 之间")
			}
			if !seen[day] {
				seen[day] = true
				days = append(days, day)
			}
		}
		if len(days) == 0 {
			return fmt.Errorf("每周计划至少选择一天")
		}
		sort.Ints(days)
		schedule.Weekdays = days
	default:
		return fmt.Errorf("不支持的计划类型: %q", schedule.Kind)
	}
	return nil
}

func normalizeBackupDestination(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("请选择备份目录")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("备份目录必须是绝对路径")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("备份目录不可用: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("备份目录不是文件夹")
	}
	return path, nil
}

// sanitizeBackupPrefix 生成仅含字母、数字、-、_ 的文件名前缀，避免路径穿越与特殊字符。
func sanitizeBackupPrefix(prefix, fallback string) string {
	clean := func(value string) string {
		var b strings.Builder
		count := 0
		lastUnderscore := false
		for _, r := range strings.TrimSpace(value) {
			if count >= backupMaxPrefixRunes {
				break
			}
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-':
				b.WriteRune(r)
				lastUnderscore = false
			case !lastUnderscore:
				b.WriteRune('_')
				lastUnderscore = true
			default:
				continue
			}
			count++
		}
		return strings.Trim(b.String(), "_-")
	}
	if value := clean(prefix); value != "" {
		return value
	}
	if value := clean(fallback); value != "" {
		return value
	}
	return "backup"
}

func cloneBackupJob(job BackupJob) BackupJob {
	out := job
	out.Targets = make([]BackupTarget, len(job.Targets))
	for i, target := range job.Targets {
		out.Targets[i] = BackupTarget{Database: target.Database, Tables: append([]string(nil), target.Tables...)}
	}
	out.Schedule.Weekdays = append([]int(nil), job.Schedule.Weekdays...)
	return out
}

func cloneBackupRun(run BackupRun) BackupRun {
	out := run
	out.Warnings = append([]string(nil), run.Warnings...)
	return out
}
