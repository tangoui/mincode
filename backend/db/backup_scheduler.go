package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"mincode/app/backend/core"
)

const (
	backupRunEvent         = "db-backup:run"
	backupMaxConcurrent    = 2
	backupStartupDelay     = 20 * time.Second
	backupMaxWait          = 30 * time.Second
	backupInterruptedError = "备份因应用退出而中断"
)

// backupClock 抽象时间，测试中使用可手动推进的假时钟。
type backupClock interface {
	Now() time.Time
	After(d time.Duration) (<-chan time.Time, func())
}

type realBackupClock struct{}

func (realBackupClock) Now() time.Time { return time.Now() }

func (realBackupClock) After(d time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(d)
	return timer.C, func() { timer.Stop() }
}

type backupActiveRun struct {
	runID  string
	cancel context.CancelFunc
}

// BackupRunEvent 推送给前端的运行事件，用于刷新列表与发送通知。
type BackupRunEvent struct {
	JobID           string    `json:"jobId"`
	JobName         string    `json:"jobName"`
	ConnectionID    string    `json:"connectionId"`
	NotifyOnSuccess bool      `json:"notifyOnSuccess"`
	Run             BackupRun `json:"run"`
}

// backupManager 持有任务、历史与调度循环。所有状态变更都在 mu 下完成并立即持久化。
type backupManager struct {
	durable bool
	mu      sync.Mutex
	store   backupStore
	clock   backupClock
	loc     *time.Location
	exec    backupExecutor
	emit    func(name string, payload any)
	jobs    []BackupJob
	history map[string][]BackupRun
	loaded  bool
	running map[string]*backupActiveRun

	ctx        context.Context
	cancel     context.CancelFunc
	started    bool
	stopped    bool
	startDelay time.Duration
	wake       chan struct{}
	wg         sync.WaitGroup
	sem        chan struct{}
}

func newBackupManager(store backupStore, clock backupClock, exec backupExecutor, emit func(string, any)) *backupManager {
	return &backupManager{
		store:   store,
		clock:   clock,
		loc:     time.Local,
		exec:    exec,
		emit:    emit,
		history: map[string][]BackupRun{},
		running: map[string]*backupActiveRun{},
		wake:    make(chan struct{}, 1),
		sem:     make(chan struct{}, backupMaxConcurrent),
	}
}

// ensureLoadedLocked 首次访问时读取持久化数据，并把上次退出时残留的 running 记录标记为失败。
func (m *backupManager) ensureLoadedLocked() error {
	if m.loaded {
		return nil
	}
	jobs, err := m.store.loadJobs()
	if err != nil {
		return err
	}
	history, err := m.store.loadHistory()
	if err != nil {
		return err
	}
	changed := false
	for jobID, runs := range history {
		for i := range runs {
			if runs[i].Status == backupStatusRunning {
				runs[i].Status = backupStatusFailed
				runs[i].Error = backupInterruptedError
				changed = true
			}
		}
		history[jobID] = runs
	}
	for i := range jobs {
		if jobs[i].State.LastStatus == backupStatusRunning {
			jobs[i].State.LastStatus = backupStatusFailed
		}
	}
	if m.durable {
		_, store, err := databaseTransferManager()
		if err != nil {
			return err
		}
		for jobID, runs := range history {
			for _, run := range runs {
				task := legacyBackupTask(jobID, run)
				if err := store.ImportLegacy(context.Background(), task); err != nil {
					return err
				}
			}
		}
	}
	m.jobs = jobs
	m.history = history
	m.loaded = true
	if changed {
		if err := m.store.saveHistory(m.history); err != nil {
			slog.Warn("database backup history repair failed", "error", err)
		}
	}
	return nil
}

// start 启动调度循环；delay 之后首次检查（包括错过的计划补跑一次）。
func (m *backupManager) start(parent context.Context, delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.stopped {
		return
	}
	m.ctx, m.cancel = context.WithCancel(parent)
	m.started = true
	m.startDelay = delay
	m.wg.Add(1)
	go m.loop()
}

func (m *backupManager) stop(ctx context.Context) {
	m.mu.Lock()
	m.stopped = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("database backup shutdown timed out")
	}
}

func (m *backupManager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *backupManager) loop() {
	defer m.wg.Done()
	if m.startDelay > 0 {
		ch, stop := m.clock.After(m.startDelay)
		select {
		case <-m.ctx.Done():
			stop()
			return
		case <-ch:
		}
	}
	for {
		wait := m.tick()
		ch, stop := m.clock.After(wait)
		select {
		case <-m.ctx.Done():
			stop()
			return
		case <-m.wake:
			stop()
		case <-ch:
		}
	}
}

// tick 启动所有到期任务，返回距下一次检查的等待时长。
func (m *backupManager) tick() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoadedLocked(); err != nil {
		slog.Warn("database backup load failed", "error", err)
		return backupMaxWait
	}
	now := m.clock.Now()
	changed := false
	for i := range m.jobs {
		job := &m.jobs[i]
		due, trigger, consumed := backupDueDecision(*job, now, m.loc)
		if !due {
			continue
		}
		occurrence := backupOccurrenceKey(*job, now, m.loc)
		previousScheduled := job.State.LastScheduledAt
		job.State.LastScheduledAt = consumed.UnixMilli()
		changed = true
		if _, busy := m.running[job.ID]; busy {
			// 同一任务不重叠执行：上一轮未结束时跳过本次计划。
			slog.Info("database backup skipped: previous run still active", "job", job.ID)
			continue
		}
		if m.durable {
			if _, err := m.submitDurableLocked(*job, trigger, occurrence); err != nil {
				job.State.LastScheduledAt = previousScheduled
				slog.Warn("backup admission failed", "job", job.ID)
			}
		} else {
			m.startRunLocked(*job, trigger)
		}
	}
	if changed {
		if err := m.store.saveJobs(m.jobs); err != nil {
			slog.Warn("database backup state save failed", "error", err)
		}
	}
	wait := backupMaxWait
	for _, job := range m.jobs {
		if !job.Enabled {
			continue
		}
		next, ok := nextBackupRun(job.Schedule, backupScheduleAnchor(job), m.loc)
		if !ok {
			continue
		}
		if d := next.Sub(now); d < wait {
			wait = d
		}
	}
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}

func (m *backupManager) findJobLocked(id string) int {
	for i := range m.jobs {
		if m.jobs[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *backupManager) startRunLocked(job BackupJob, trigger string) BackupRun {
	now := m.clock.Now()
	run := BackupRun{
		ID: uuid.NewString(), JobID: job.ID, Trigger: trigger,
		Status: backupStatusRunning, StartedAt: now.UnixMilli(),
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.running[job.ID] = &backupActiveRun{runID: run.ID, cancel: cancel}
	m.appendHistoryLocked(run)
	m.emitRunLocked(job, run)
	m.wg.Add(1)
	go m.execute(ctx, cancel, cloneBackupJob(job), run, now)
	return run
}

func (m *backupManager) appendHistoryLocked(run BackupRun) {
	runs := append([]BackupRun{run}, m.history[run.JobID]...)
	if len(runs) > backupHistoryLimit {
		runs = runs[:backupHistoryLimit]
	}
	m.history[run.JobID] = runs
	if err := m.store.saveHistory(m.history); err != nil {
		slog.Warn("database backup history save failed", "error", err)
	}
}

func (m *backupManager) emitRunLocked(job BackupJob, run BackupRun) {
	if m.emit == nil {
		return
	}
	m.emit(backupRunEvent, BackupRunEvent{
		JobID: job.ID, JobName: job.Name, ConnectionID: job.ConnectionID,
		NotifyOnSuccess: job.NotifyOnSuccess, Run: cloneBackupRun(run),
	})
}

func (m *backupManager) execute(ctx context.Context, cancel context.CancelFunc, job BackupJob, run BackupRun, startedAt time.Time) {
	defer m.wg.Done()
	defer cancel()
	var (
		result backupResult
		err    error
	)
	select {
	case m.sem <- struct{}{}:
		result, err = m.runExecutor(ctx, job, startedAt)
		<-m.sem
	case <-ctx.Done():
		err = ctx.Err()
	}
	finished := m.clock.Now()
	run.FinishedAt = finished.UnixMilli()
	run.DurationMs = finished.Sub(startedAt).Milliseconds()
	run.Tables = result.Tables
	run.Rows = result.Rows
	run.Warnings = result.Warnings
	switch {
	case err == nil:
		run.Status = backupStatusSuccess
		run.File = result.File
		run.SizeBytes = result.SizeBytes
		run.Removed = result.Removed
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		run.Status = backupStatusCancelled
		run.Error = "备份已取消"
	default:
		run.Status = backupStatusFailed
		run.Error = err.Error()
	}
	m.finishRun(job, run)
}

// runExecutor 调用执行器并把 panic 转为失败，避免调度器被单次备份拖垮。
func (m *backupManager) runExecutor(ctx context.Context, job BackupJob, startedAt time.Time) (result backupResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("备份执行异常: %v", recovered)
		}
	}()
	return m.exec(ctx, job, startedAt)
}

func (m *backupManager) finishRun(job BackupJob, run BackupRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if active, ok := m.running[job.ID]; ok && active.runID == run.ID {
		delete(m.running, job.ID)
	}
	runs := m.history[job.ID]
	for i := range runs {
		if runs[i].ID == run.ID {
			runs[i] = run
			break
		}
	}
	if index := m.findJobLocked(job.ID); index >= 0 {
		m.jobs[index].State.LastRunAt = run.StartedAt
		m.jobs[index].State.LastStatus = run.Status
		job = m.jobs[index]
		if err := m.store.saveJobs(m.jobs); err != nil {
			slog.Warn("database backup state save failed", "error", err)
		}
		if err := m.store.saveHistory(m.history); err != nil {
			slog.Warn("database backup history save failed", "error", err)
		}
	}
	m.emitRunLocked(job, run)
	m.signal()
}

// ── 对外操作 ──

func (m *backupManager) viewLocked(job BackupJob) BackupJobView {
	view := BackupJobView{BackupJob: cloneBackupJob(job)}
	if job.Enabled {
		if next, ok := nextBackupRun(job.Schedule, backupScheduleAnchor(job), m.loc); ok {
			view.NextRunAt = next.UnixMilli()
		}
	}
	_, view.Running = m.running[job.ID]
	if m.durable {
		view.Running = m.durableBusyLocked(job.ID)
		if runs, err := m.durableRunsLocked(job.ID); err == nil && len(runs) > 0 {
			last := runs[0]
			view.LastRun = &last
			return view
		}
	}
	if runs := m.history[job.ID]; len(runs) > 0 {
		last := cloneBackupRun(runs[0])
		view.LastRun = &last
	}
	return view
}

func (m *backupManager) list() ([]BackupJobView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	views := make([]BackupJobView, 0, len(m.jobs))
	for _, job := range m.jobs {
		views = append(views, m.viewLocked(job))
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].CreatedAt < views[j].CreatedAt })
	return views, nil
}

func (m *backupManager) save(input BackupJob) (BackupJobView, error) {
	if err := normalizeBackupJob(&input); err != nil {
		return BackupJobView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoadedLocked(); err != nil {
		return BackupJobView{}, err
	}
	now := m.clock.Now()
	job := input
	job.UpdatedAt = now.UnixMilli()
	if job.ID == "" {
		if len(m.jobs) >= backupMaxJobs {
			return BackupJobView{}, fmt.Errorf("备份任务数量已达上限 %d", backupMaxJobs)
		}
		job.ID = uuid.NewString()
		job.CreatedAt = job.UpdatedAt
		job.State = BackupJobState{}
		m.jobs = append(m.jobs, job)
	} else {
		index := m.findJobLocked(job.ID)
		if index < 0 {
			return BackupJobView{}, fmt.Errorf("备份任务不存在")
		}
		job.CreatedAt = m.jobs[index].CreatedAt
		job.State = m.jobs[index].State
		m.jobs[index] = job
	}
	if err := m.store.saveJobs(m.jobs); err != nil {
		return BackupJobView{}, err
	}
	m.signal()
	return m.viewLocked(job), nil
}

func (m *backupManager) remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoadedLocked(); err != nil {
		return err
	}
	index := m.findJobLocked(id)
	if index < 0 {
		return fmt.Errorf("备份任务不存在")
	}
	if active, ok := m.running[id]; ok {
		active.cancel()
	}
	if m.durable {
		m.cancelDurable(id)
	}
	m.jobs = append(m.jobs[:index], m.jobs[index+1:]...)
	delete(m.history, id)
	if err := m.store.saveJobs(m.jobs); err != nil {
		return err
	}
	if err := m.store.saveHistory(m.history); err != nil {
		return err
	}
	m.signal()
	return nil
}

func (m *backupManager) runNow(id string, requestKey ...string) (BackupRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.stopped || m.ctx == nil {
		return BackupRun{}, fmt.Errorf("备份调度器未运行")
	}
	if err := m.ensureLoadedLocked(); err != nil {
		return BackupRun{}, err
	}
	index := m.findJobLocked(id)
	if index < 0 {
		return BackupRun{}, fmt.Errorf("备份任务不存在")
	}
	if _, busy := m.running[id]; busy {
		return BackupRun{}, fmt.Errorf("该备份任务正在运行")
	}
	if m.durable {
		return m.submitDurableLocked(m.jobs[index], backupTriggerManual, requestKey...)
	}
	return m.startRunLocked(m.jobs[index], backupTriggerManual), nil
}

func (m *backupManager) cancelRun(id string) bool {
	if m.durable {
		return m.cancelDurable(id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	active, ok := m.running[id]
	if ok {
		active.cancel()
	}
	return ok
}

func (m *backupManager) runs(id string) ([]BackupRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	if m.durable {
		return m.durableRunsLocked(id)
	}
	runs := m.history[id]
	out := make([]BackupRun, len(runs))
	for i, run := range runs {
		out[i] = cloneBackupRun(run)
	}
	return out, nil
}

// ── 进程级实例与生命周期 ──

var (
	backupMgrMu sync.Mutex
	backupMgr   *backupManager
)

func defaultBackupManager() *backupManager {
	backupMgrMu.Lock()
	defer backupMgrMu.Unlock()
	if backupMgr == nil {
		backupMgr = newBackupManager(kvBackupStore{}, realBackupClock{}, executeBackupJob, core.Emit)
		backupMgr.durable = true
	}
	return backupMgr
}

// StartBackupScheduler 在应用启动时调用（非阻塞）：延迟片刻后检查到期任务，错过的计划补跑一次。
func StartBackupScheduler(ctx context.Context) {
	if _, _, err := databaseTransferManager(); err != nil {
		slog.Warn("database transfer startup failed")
		return
	}
	defaultBackupManager().start(context.WithoutCancel(ctx), backupStartupDelay)
}

// StopBackupScheduler 在应用退出时调用：取消进行中的备份并等待其清理临时文件、写入历史。
func StopBackupScheduler(ctx context.Context) {
	backupMgrMu.Lock()
	mgr := backupMgr
	backupMgrMu.Unlock()
	if mgr != nil {
		mgr.stop(ctx)
	}
}

// activeBackupManager 供命令使用；若启动流程尚未调用，则按需启动调度器。
func activeBackupManager() *backupManager {
	mgr := defaultBackupManager()
	mgr.start(context.Background(), backupStartupDelay)
	return mgr
}
