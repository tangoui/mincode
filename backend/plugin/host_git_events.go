package plugin

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// git.headChanged: while at least one running plugin subscribes, the host
// checks the workspace root's HEAD every few seconds and publishes
// { branch, detached, commit } when it changes (checkout, branch switch,
// detaching). Nothing runs until the first subscription.
const gitHeadPollInterval = 3 * time.Second

var gitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type gitHeadState struct {
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Commit   string `json:"commit,omitempty"`
}

var gitHeadWatcher struct {
	once sync.Once
}

func (manager *pluginManager) ensureGitHeadWatcher() {
	gitHeadWatcher.once.Do(func() {
		go manager.watchGitHead(time.NewTicker(gitHeadPollInterval).C)
	})
}

func (manager *pluginManager) watchGitHead(ticks <-chan time.Time) {
	last := ""
	for range ticks {
		last = manager.checkGitHead(last)
	}
}

// checkGitHead publishes when the (root, HEAD) pair differs from last and
// returns the new pair. With no subscriber it forgets the state, so a later
// subscriber is not sent a stale change.
func (manager *pluginManager) checkGitHead(last string) string {
	if !manager.anySubscribed(hostEventGitHeadChanged) {
		return ""
	}
	root := manager.workspaceRootPath()
	if root == "" {
		return ""
	}
	state, ok := readGitHead(root)
	if !ok {
		return root + "\x00"
	}
	key := root + "\x00" + state.Branch + "\x00" + state.Commit
	if last != "" && key != last {
		manager.publishHostEvent(hostEventGitHeadChanged, state)
	}
	return key
}

func (manager *pluginManager) anySubscribed(event string) bool {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	for _, record := range records {
		record.mu.Lock()
		runtimeHost := record.runtime
		running := runtimeHost != nil && record.state == pluginStateRunning
		record.mu.Unlock()
		if running && runtimeHost.subscribedTo(event) {
			return true
		}
	}
	return false
}

// readGitHead reads HEAD of the repository at root, following the
// "gitdir:" file of linked worktrees and submodules.
func readGitHead(root string) (gitHeadState, bool) {
	gitDir := filepath.Join(root, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return gitHeadState{}, false
	}
	if !info.IsDir() {
		data, err := os.ReadFile(gitDir)
		if err != nil || len(data) > 4096 {
			return gitHeadState{}, false
		}
		target, found := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !found {
			return gitHeadState{}, false
		}
		target = strings.TrimSpace(target)
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		gitDir = filepath.Clean(target)
	}
	file, err := os.Open(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return gitHeadState{}, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return gitHeadState{}, false
	}
	line := strings.TrimSpace(scanner.Text())
	if ref, found := strings.CutPrefix(line, "ref: "); found {
		branch, isBranch := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
		if !isBranch || branch == "" || len(branch) > 255 {
			return gitHeadState{}, false
		}
		return gitHeadState{Branch: branch}, true
	}
	if gitSHAPattern.MatchString(line) {
		return gitHeadState{Detached: true, Commit: line[:12]}, true
	}
	return gitHeadState{}, false
}
