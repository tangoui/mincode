// Package configsync manages an app-owned Git worktree used to synchronize an
// encrypted configuration snapshot. Plain configuration and credentials never
// enter the worktree: the frontend sends one opaque encrypted payload, while
// Git credentials are supplied only to the child process environment.
package configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"mincode/app/backend/core"
)

const (
	payloadFilename = "mincode.config.enc"
	maxPayloadBytes = 10 * 1024 * 1024
	gitTimeout      = 120 * time.Second
	gitWaitDelay    = 2 * time.Second
	gitStdoutLimit  = maxPayloadBytes + 1024*1024
	gitStderrLimit  = 64 * 1024
)

var (
	branchPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
	scpRemotePattern = regexp.MustCompile(`^[^@\s]+@[^:\s]+:[^\s]+$`)
	userDataDir      = core.UserDataDir
	allowLocalRemote = false
)

type repositoryCredentials struct {
	Mode     string `json:"mode"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

type repositoryArgs struct {
	RemoteURL   string                `json:"remoteUrl"`
	Branch      string                `json:"branch"`
	Credentials repositoryCredentials `json:"credentials"`
}

type pushArgs struct {
	RemoteURL   string                `json:"remoteUrl"`
	Branch      string                `json:"branch"`
	Credentials repositoryCredentials `json:"credentials"`
	Payload     string                `json:"payload"`
}

type connectionResult struct {
	OK              bool   `json:"ok"`
	DefaultBranch   string `json:"default_branch"`
	BranchExists    bool   `json:"branch_exists"`
	RepositoryEmpty bool   `json:"repository_empty"`
}

type pullResult struct {
	Exists    bool   `json:"exists"`
	Payload   string `json:"payload"`
	Commit    string `json:"commit"`
	UpdatedAt int64  `json:"updated_at"`
}

type pushResult struct {
	Changed bool   `json:"changed"`
	Commit  string `json:"commit"`
}

func init() {
	core.Register("config_sync_test_connection", testConnectionHandler)
	core.Register("config_sync_pull", pullHandler)
	core.Register("config_sync_push", pushHandler)
}

func testConnectionHandler(_, argsJSON string) (string, error) {
	var args repositoryArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := validateRepositoryArgs(&args); err != nil {
		return "", err
	}
	stdout, err := runRemoteGit(args.Credentials, "ls-remote", "--symref", args.RemoteURL, "HEAD", "refs/heads/"+args.Branch)
	if err != nil {
		return "", fmt.Errorf("连接 Git 仓库: %w", err)
	}
	defaultBranch := parseDefaultBranch(stdout)
	branchExists := strings.Contains(stdout, "refs/heads/"+args.Branch)
	repositoryEmpty := strings.TrimSpace(stdout) == ""
	return core.EncodeResult(connectionResult{
		OK:              true,
		DefaultBranch:   defaultBranch,
		BranchExists:    branchExists,
		RepositoryEmpty: repositoryEmpty,
	})
}

func pullHandler(_, argsJSON string) (string, error) {
	var args repositoryArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := validateRepositoryArgs(&args); err != nil {
		return "", err
	}
	repoPath, err := ensureRepository(args)
	if err != nil {
		return "", err
	}
	exists, err := remoteBranchExists(args)
	if err != nil {
		return "", err
	}
	if !exists {
		return core.EncodeResult(pullResult{})
	}
	if _, err := runRepoGit(repoPath, args.Credentials, "fetch", "--prune", "origin", "refs/heads/"+args.Branch+":refs/remotes/origin/"+args.Branch); err != nil {
		return "", fmt.Errorf("获取远程配置: %w", err)
	}
	ref := "refs/remotes/origin/" + args.Branch
	payload, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "show", ref+":"+payloadFilename)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return core.EncodeResult(pullResult{})
		}
		return "", fmt.Errorf("读取远程配置: %w", err)
	}
	if len(payload) > maxPayloadBytes {
		return "", errors.New("远程配置文件超过 10 MB 限制")
	}
	commit, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("读取远程提交: %w", err)
	}
	updatedAtText, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "show", "-s", "--format=%ct", ref)
	if err != nil {
		return "", fmt.Errorf("读取远程提交时间: %w", err)
	}
	var updatedAt int64
	_, _ = fmt.Sscan(strings.TrimSpace(updatedAtText), &updatedAt)
	return core.EncodeResult(pullResult{
		Exists:    true,
		Payload:   strings.TrimSpace(payload),
		Commit:    strings.TrimSpace(commit),
		UpdatedAt: updatedAt,
	})
}

func pushHandler(_, argsJSON string) (string, error) {
	var args pushArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	repository := repositoryArgs{RemoteURL: args.RemoteURL, Branch: args.Branch, Credentials: args.Credentials}
	if err := validateRepositoryArgs(&repository); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Payload) == "" {
		return "", errors.New("加密配置内容为空")
	}
	if len(args.Payload) > maxPayloadBytes {
		return "", errors.New("加密配置文件超过 10 MB 限制")
	}
	repoPath, err := ensureRepository(repository)
	if err != nil {
		return "", err
	}
	exists, err := remoteBranchExists(repository)
	if err != nil {
		return "", err
	}
	if exists {
		if _, err := runRepoGit(repoPath, args.Credentials, "fetch", "--prune", "origin", "refs/heads/"+args.Branch+":refs/remotes/origin/"+args.Branch); err != nil {
			return "", fmt.Errorf("获取远程配置: %w", err)
		}
		if _, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "reset", "--hard", "refs/remotes/origin/"+args.Branch); err != nil {
			return "", fmt.Errorf("更新本地同步副本: %w", err)
		}
	} else {
		if _, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "checkout", "--orphan", args.Branch); err != nil {
			// A fresh repository may already point HEAD at the requested unborn branch.
			if _, symbolicErr := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "symbolic-ref", "HEAD", "refs/heads/"+args.Branch); symbolicErr != nil {
				return "", fmt.Errorf("创建同步分支: %w", err)
			}
		}
		_, _ = runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "rm", "-rf", "--ignore-unmatch", ".")
	}
	if err := writePayload(repoPath, args.Payload); err != nil {
		return "", err
	}
	if _, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "add", "--", payloadFilename, ".gitattributes"); err != nil {
		return "", fmt.Errorf("暂存配置快照: %w", err)
	}
	changed, err := gitDiffCachedChanged(repoPath)
	if err != nil {
		return "", fmt.Errorf("检查配置快照差异: %w", err)
	}
	if !changed {
		commit, _ := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "rev-parse", "HEAD")
		return core.EncodeResult(pushResult{Changed: false, Commit: strings.TrimSpace(commit)})
	}
	if _, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "-c", "user.name=MinCode", "-c", "user.email=config-sync@mincode.local", "commit", "-m", "chore: sync MinCode configuration"); err != nil {
		return "", fmt.Errorf("提交配置快照: %w", err)
	}
	if _, err := runRepoGit(repoPath, args.Credentials, "push", "--set-upstream", "origin", "HEAD:refs/heads/"+args.Branch); err != nil {
		return "", fmt.Errorf("推送配置快照: %w", err)
	}
	commit, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取同步提交: %w", err)
	}
	return core.EncodeResult(pushResult{Changed: true, Commit: strings.TrimSpace(commit)})
}

func validateRepositoryArgs(args *repositoryArgs) error {
	args.RemoteURL = strings.TrimSpace(args.RemoteURL)
	args.Branch = strings.TrimSpace(args.Branch)
	args.Credentials.Mode = strings.TrimSpace(args.Credentials.Mode)
	if args.Branch == "" {
		args.Branch = "main"
	}
	if err := validateRemoteURL(args.RemoteURL); err != nil {
		return err
	}
	if !branchPattern.MatchString(args.Branch) || strings.Contains(args.Branch, "..") || strings.Contains(args.Branch, "//") || strings.Contains(args.Branch, "@{") || strings.HasSuffix(args.Branch, "/") {
		return errors.New("Git 分支名无效")
	}
	switch args.Credentials.Mode {
	case "", "system", "ssh":
		args.Credentials.Mode = "system"
	case "https-token":
		if !strings.HasPrefix(strings.ToLower(args.RemoteURL), "https://") {
			return errors.New("访问令牌认证仅支持 HTTPS 仓库地址")
		}
		if strings.TrimSpace(args.Credentials.Username) == "" || args.Credentials.Token == "" {
			return errors.New("用户名和访问令牌不能为空")
		}
	default:
		return errors.New("不支持的 Git 认证方式")
	}
	return nil
}

func validateRemoteURL(remote string) error {
	if remote == "" || len(remote) > 2048 || strings.ContainsAny(remote, "\r\n\x00") {
		return errors.New("Git 仓库地址无效")
	}
	if allowLocalRemote && (filepath.IsAbs(remote) || strings.HasPrefix(remote, "file://")) {
		return nil
	}
	if scpRemotePattern.MatchString(remote) {
		return nil
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Host == "" {
		return errors.New("仅支持 HTTPS 或 SSH Git 仓库地址")
	}
	if parsed.User != nil {
		return errors.New("仓库地址不能包含用户名、密码或访问令牌")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "ssh" {
		return errors.New("仅支持 HTTPS 或 SSH Git 仓库地址")
	}
	return nil
}

func parseDefaultBranch(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			return strings.TrimPrefix(fields[1], "refs/heads/")
		}
	}
	return ""
}

func repositoryPath(remoteURL, branch string) (string, error) {
	root := userDataDir()
	if root == "" {
		return "", errors.New("应用数据目录不可用")
	}
	digest := sha256.Sum256([]byte(remoteURL + "\x00" + branch))
	name := hex.EncodeToString(digest[:12])
	return filepath.Join(root, appDataDirectory, "config-sync", name), nil
}

func ensureRepository(args repositoryArgs) (string, error) {
	repoPath, err := repositoryPath(args.RemoteURL, args.Branch)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(repoPath, 0o700); err != nil {
		return "", fmt.Errorf("创建配置同步目录: %w", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "init"); err != nil {
			return "", fmt.Errorf("初始化配置同步仓库: %w", err)
		}
	}
	remote, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "remote", "get-url", "origin")
	if err != nil {
		if _, addErr := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "remote", "add", "origin", args.RemoteURL); addErr != nil {
			return "", fmt.Errorf("添加配置同步远程仓库: %w", addErr)
		}
	} else if strings.TrimSpace(remote) != args.RemoteURL {
		if _, err := runRepoGit(repoPath, repositoryCredentials{Mode: "system"}, "remote", "set-url", "origin", args.RemoteURL); err != nil {
			return "", fmt.Errorf("更新配置同步远程仓库: %w", err)
		}
	}
	return repoPath, nil
}

func remoteBranchExists(args repositoryArgs) (bool, error) {
	stdout, err := runRemoteGit(args.Credentials, "ls-remote", "--heads", args.RemoteURL, "refs/heads/"+args.Branch)
	if err != nil {
		return false, fmt.Errorf("检查远程同步分支: %w", err)
	}
	return strings.TrimSpace(stdout) != "", nil
}

func writePayload(repoPath, payload string) error {
	if err := os.WriteFile(filepath.Join(repoPath, payloadFilename), []byte(strings.TrimSpace(payload)+"\n"), 0o600); err != nil {
		return fmt.Errorf("写入加密配置快照: %w", err)
	}
	attributes := payloadFilename + " binary\n"
	if err := os.WriteFile(filepath.Join(repoPath, ".gitattributes"), []byte(attributes), 0o600); err != nil {
		return fmt.Errorf("写入 Git 属性: %w", err)
	}
	return nil
}

func gitDiffCachedChanged(repoPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, cleanup, err := gitCommand(ctx, repoPath, repositoryCredentials{Mode: "system"}, "diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	defer cleanup()
	err = cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return false, errors.New("Git 操作超时")
	}
	return false, err
}

func runRemoteGit(credentials repositoryCredentials, args ...string) (string, error) {
	return runGit("", credentials, args...)
}

func runRepoGit(repoPath string, credentials repositoryCredentials, args ...string) (string, error) {
	return runGit(repoPath, credentials, args...)
}

func runGit(repoPath string, credentials repositoryCredentials, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return runGitContext(ctx, repoPath, credentials, args...)
}

// runGitContext 只返回 stdout；stderr 单独收集用于错误信息，避免警告混入载荷或提交号。
func runGitContext(ctx context.Context, repoPath string, credentials repositoryCredentials, args ...string) (string, error) {
	cmd, cleanup, err := gitCommand(ctx, repoPath, credentials, args...)
	if err != nil {
		return "", err
	}
	defer cleanup()
	stdout := core.NewCappedWriter(gitStdoutLimit)
	stderr := core.NewCappedWriter(gitStderrLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", errors.New("Git 操作超时")
	}
	// 进程已成功退出、仅有后代进程占用输出管道时视为成功。
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	output, truncated := stdout.Bytes()
	if truncated {
		return "", errors.New("Git 输出超过允许大小")
	}
	if err != nil {
		errorOutput, _ := stderr.Bytes()
		message := strings.TrimSpace(string(errorOutput))
		if message == "" {
			message = core.TakeChars(strings.TrimSpace(string(output)), 4096)
		}
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	return string(output), nil
}

func gitCommand(ctx context.Context, repoPath string, credentials repositoryCredentials, args ...string) (*exec.Cmd, func(), error) {
	gitPath, err := findGitExecutable()
	if err != nil {
		return nil, func() {}, err
	}
	commandArgs := make([]string, 0, len(args)+2)
	if repoPath != "" {
		commandArgs = append(commandArgs, "-C", repoPath)
	}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, gitPath, commandArgs...)
	// 超时需结束整个进程树：ssh / git-remote-https 等后代进程会继续持有输出管道。
	core.PrepareProcessTree(cmd)
	cmd.Cancel = func() error { return core.KillProcessTree(cmd) }
	cmd.WaitDelay = gitWaitDelay
	// 固定 C locale，保证按英文 Git 输出判断错误类型。
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANGUAGE=C")
	cleanup := func() {}
	if credentials.Mode == "https-token" {
		helperPath, remove, err := createAskPassHelper()
		if err != nil {
			return nil, func() {}, err
		}
		cleanup = remove
		cmd.Env = append(cmd.Env,
			"GIT_ASKPASS="+helperPath,
			"MINCODE_GIT_USERNAME="+credentials.Username,
			"MINCODE_GIT_TOKEN="+credentials.Token,
		)
	}
	return cmd, cleanup, nil
}

func findGitExecutable() (string, error) {
	if path, err := exec.LookPath("git"); err == nil {
		return path, nil
	}
	for _, candidate := range []string{"/usr/bin/git", "/opt/homebrew/bin/git", "/usr/local/bin/git"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("未找到 Git CLI")
}

func createAskPassHelper() (string, func(), error) {
	dir, err := os.MkdirTemp("", "mincode-git-askpass-")
	if err != nil {
		return "", func() {}, fmt.Errorf("创建 Git 凭证助手目录: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	name, content := askPassHelperScript(runtime.GOOS)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("创建 Git 凭证助手: %w", err)
	}
	return path, cleanup, nil
}

func askPassHelperScript(goos string) (name, content string) {
	if goos == "windows" {
		// 延迟扩展的值不会再被 cmd 解析，凭证中的 & | < > ^ ) 等字符按原样输出；echo( 保证空值输出空行。
		return "askpass.cmd", "@echo off\r\nsetlocal EnableDelayedExpansion\r\necho %1 | findstr /I username >nul\r\nif %errorlevel%==0 (echo(!MINCODE_GIT_USERNAME!) else (echo(!MINCODE_GIT_TOKEN!)\r\n"
	}
	return "askpass.sh", "#!/bin/sh\ncase \"$1\" in *sername*) printf '%s\\n' \"$MINCODE_GIT_USERNAME\" ;; *) printf '%s\\n' \"$MINCODE_GIT_TOKEN\" ;; esac\n"
}
