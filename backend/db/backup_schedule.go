package db

import (
	"strconv"
	"time"
)

// backupLateThreshold 超过该时长仍未执行的计划视为错过，按补跑（catchup）执行一次。
const backupLateThreshold = 5 * time.Minute

// backupScheduleAnchor 返回计算下一次运行的锚点：最近一次消费的计划时刻与任务更新时间中较晚者。
func backupScheduleAnchor(job BackupJob) time.Time {
	anchor := job.State.LastScheduledAt
	if job.UpdatedAt > anchor {
		anchor = job.UpdatedAt
	}
	return time.UnixMilli(anchor)
}

// nextBackupRun 计算锚点之后（严格大于）的下一次计划时刻，按 loc 本地时间解释 HH:MM。
func nextBackupRun(schedule BackupSchedule, anchor time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.Local
	}
	switch schedule.Kind {
	case backupScheduleInterval:
		if schedule.IntervalHours < 1 {
			return time.Time{}, false
		}
		return anchor.Add(time.Duration(schedule.IntervalHours) * time.Hour), true
	case backupScheduleDaily, backupScheduleWeekly:
		hour, minute, ok := parseBackupClock(schedule.Time)
		if !ok {
			return time.Time{}, false
		}
		allowed := map[time.Weekday]bool{}
		if schedule.Kind == backupScheduleWeekly {
			for _, day := range schedule.Weekdays {
				if day >= 0 && day <= 6 {
					allowed[time.Weekday(day)] = true
				}
			}
			if len(allowed) == 0 {
				return time.Time{}, false
			}
		}
		local := anchor.In(loc)
		for offset := 0; offset <= 8; offset++ {
			// time.Date 会规范化越界日期；夏令时跳变时由标准库决定实际时刻。
			candidate := time.Date(local.Year(), local.Month(), local.Day()+offset, hour, minute, 0, 0, loc)
			if !candidate.After(anchor) {
				continue
			}
			if schedule.Kind == backupScheduleWeekly && !allowed[candidate.Weekday()] {
				continue
			}
			return candidate, true
		}
	}
	return time.Time{}, false
}

func parseBackupClock(value string) (int, int, bool) {
	if !backupTimePattern.MatchString(value) {
		return 0, 0, false
	}
	hour, err := strconv.Atoi(value[:2])
	if err != nil {
		return 0, 0, false
	}
	minute, err := strconv.Atoi(value[3:])
	if err != nil {
		return 0, 0, false
	}
	return hour, minute, true
}

// backupDueDecision 判断任务在 now 时是否到期；到期时返回触发类型与新的锚点。
// 迟到超过阈值（如应用未运行、系统休眠）只补跑一次，并以 now 作为新锚点，避免连续补跑。
func backupDueDecision(job BackupJob, now time.Time, loc *time.Location) (due bool, trigger string, consumed time.Time) {
	if !job.Enabled {
		return false, "", time.Time{}
	}
	next, ok := nextBackupRun(job.Schedule, backupScheduleAnchor(job), loc)
	if !ok || next.After(now) {
		return false, "", time.Time{}
	}
	if now.Sub(next) > backupLateThreshold {
		return true, backupTriggerCatchup, now
	}
	return true, backupTriggerSchedule, next
}
