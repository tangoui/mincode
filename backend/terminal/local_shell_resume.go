package terminal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"mincode/app/backend/core"
)

type terminalResumeRecord struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

var terminalResumeMu sync.Mutex

func init() {
	core.Register("take_local_shell_resume", func(_, _ string) (string, error) {
		records, err := takeTerminalResumeRecords()
		if err != nil {
			return "", err
		}
		return core.EncodeResult(records)
	})
}

// SaveLocalShellResume snapshots live desktop terminals before shutdown closes their PTYs.
func SaveLocalShellResume() error {
	return writeTerminalResumeRecords(captureTerminalResume())
}

func captureTerminalResume() map[string]terminalResumeRecord {
	sessions.mu.Lock()
	live := make(map[string]*shellProcess, len(sessions.procs))
	for id, proc := range sessions.procs {
		live[id] = proc
	}
	sessions.mu.Unlock()

	records := make(map[string]terminalResumeRecord)
	for id, proc := range live {
		proc.mu.Lock()
		// ConPTY on Windows has no reliable foreground process query.
		pty, ok := proc.localPTY.(interface {
			resumeRecord() (terminalResumeRecord, bool)
		})
		if ok {
			if record, ok := pty.resumeRecord(); ok {
				records[id] = record
			}
		}
		proc.mu.Unlock()
	}
	return records
}

func terminalResumePath() (string, error) {
	dir := core.UserDataDir()
	if dir == "" {
		return "", errors.New("terminal resume data directory is unavailable")
	}
	return filepath.Join(dir, core.AppDataNamespace, "terminal-resume.json"), nil
}

func writeTerminalResumeRecords(records map[string]terminalResumeRecord) error {
	terminalResumeMu.Lock()
	defer terminalResumeMu.Unlock()

	path, err := terminalResumePath()
	if err != nil {
		return err
	}
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".terminal-resume-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(records)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func takeTerminalResumeRecords() (map[string]terminalResumeRecord, error) {
	terminalResumeMu.Lock()
	defer terminalResumeMu.Unlock()

	path, err := terminalResumePath()
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("terminal resume file must be private and regular")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	var records map[string]terminalResumeRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("decode terminal resume file: %w", err)
	}
	for id, record := range records {
		dir, err := os.Stat(record.Cwd)
		if err != nil || !dir.IsDir() || !filepath.IsAbs(record.Cwd) {
			delete(records, id)
		}
	}
	return records, nil
}
