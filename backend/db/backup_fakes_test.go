package db

import (
	"sync"
	"testing"
	"time"
)

// memoryBackupStore 用于测试的内存存储。
type memoryBackupStore struct {
	mu      sync.Mutex
	jobs    []BackupJob
	history map[string][]BackupRun
	saves   int
}

func (m *memoryBackupStore) loadJobs() ([]BackupJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BackupJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		out = append(out, cloneBackupJob(job))
	}
	return out, nil
}

func (m *memoryBackupStore) saveJobs(jobs []BackupJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = make([]BackupJob, 0, len(jobs))
	for _, job := range jobs {
		m.jobs = append(m.jobs, cloneBackupJob(job))
	}
	m.saves++
	return nil
}

func (m *memoryBackupStore) loadHistory() (map[string][]BackupRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneBackupHistory(m.history), nil
}

func (m *memoryBackupStore) saveHistory(history map[string][]BackupRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = cloneBackupHistory(history)
	return nil
}

func cloneBackupHistory(history map[string][]BackupRun) map[string][]BackupRun {
	out := make(map[string][]BackupRun, len(history))
	for id, runs := range history {
		copied := make([]BackupRun, len(runs))
		for i, run := range runs {
			copied[i] = cloneBackupRun(run)
		}
		out[id] = copied
	}
	return out
}

// fakeBackupClock 可手动推进的时钟；After 返回的通道在 Advance 越过到期时刻时触发。
type fakeBackupClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeBackupTimer
}

type fakeBackupTimer struct {
	at      time.Time
	ch      chan time.Time
	stopped bool
	fired   bool
}

func newFakeBackupClock(now time.Time) *fakeBackupClock {
	return &fakeBackupClock{now: now}
}

func (c *fakeBackupClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeBackupClock) After(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeBackupTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		timer.fired = true
		timer.ch <- c.now
	}
	c.timers = append(c.timers, timer)
	return timer.ch, func() {
		c.mu.Lock()
		timer.stopped = true
		c.mu.Unlock()
	}
}

// Advance 推进时钟并触发到期计时器。
func (c *fakeBackupClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, timer := range c.timers {
		if !timer.stopped && !timer.fired && !timer.at.After(c.now) {
			timer.fired = true
			timer.ch <- c.now
		}
	}
}

// waitPending 等待调度循环注册下一个计时器（即完成一次 tick）。
func (c *fakeBackupClock) waitPending(t *testing.T) time.Duration {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, timer := range c.timers {
			if !timer.stopped && !timer.fired {
				wait := timer.at.Sub(c.now)
				c.mu.Unlock()
				return wait
			}
		}
		c.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("scheduler did not register a timer")
	return 0
}
