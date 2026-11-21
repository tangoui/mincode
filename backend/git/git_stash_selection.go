package git

import (
	"fmt"
	"os"
	"strings"
)

// gitCreateStash stores either every current change (paths == nil) or exactly the
// requested repository-relative paths. Pathspecs are sent over stdin as NUL-delimited
// literal values so spaces, newlines and Git wildcard characters cannot affect other files.
func gitCreateStash(repoPath, message, emptyLabel string, paths *[]string) ([]gitFileEntry, error) {
	status, err := gitGetStatus(repoPath)
	if err != nil {
		return nil, err
	}
	files, selectedPaths, err := gitSelectChangedFiles(status, paths)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("当前没有可%s的更改", emptyLabel)
	}

	args := []string{"stash", "push", "--include-untracked", "-m", message}
	if paths == nil {
		output, runErr := runGit(repoPath, gitDefaultTimeout, args...)
		if runErr != nil {
			return nil, runErr
		}
		if err := gitEnsureSuccess(output, "git stash push"); err != nil {
			return nil, err
		}
		return files, nil
	}

	// Git 的 pathspec 只限制工作树清理范围；默认仍会把真实 Index 中所有已暂存
	// 文件写入 Stash。用临时 Index 从 HEAD 构造仅含所选路径的快照，避免未选但
	// 已暂存的文件混入 Stash，同时保持真实 Index 原样，直到 Stash 成功创建。
	indexFile, err := os.CreateTemp("", "mincode-git-index-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时 Git Index 失败：%w", err)
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		_ = os.Remove(indexPath)
		return nil, fmt.Errorf("关闭临时 Git Index 失败：%w", err)
	}
	// read-tree 要求目标 Index 不存在或已经是有效格式，删除空占位文件后再初始化。
	if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("初始化临时 Git Index 失败：%w", err)
	}
	defer os.Remove(indexPath)

	indexEnv := map[string]string{"GIT_INDEX_FILE": indexPath}
	initialized, runErr := runGitStdinEnv(repoPath, "", gitDefaultTimeout, indexEnv, "read-tree", "HEAD")
	if runErr != nil {
		return nil, runErr
	}
	if err := gitEnsureSuccess(initialized, "git read-tree"); err != nil {
		return nil, err
	}
	pathspecInput := gitLiteralPathspecInput(selectedPaths)
	staged, runErr := runGitStdinEnv(repoPath, pathspecInput, gitDefaultTimeout, indexEnv,
		"add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
	if runErr != nil {
		return nil, runErr
	}
	if err := gitEnsureSuccess(staged, "git add selected paths"); err != nil {
		return nil, err
	}

	args = append(args, "--pathspec-from-file=-", "--pathspec-file-nul")
	output, runErr := runGitStdinEnv(repoPath, pathspecInput, gitDefaultTimeout, indexEnv, args...)
	if runErr != nil {
		return nil, runErr
	}
	if err := gitEnsureSuccess(output, "git stash push"); err != nil {
		return nil, err
	}

	// 临时 Index 已让所选路径从工作树回到 HEAD；同步清理真实 Index 中这些路径，
	// 未选路径（包括其暂存状态）不在 pathspec 中，因此保持不变。
	reset, runErr := runGitStdin(repoPath, pathspecInput, gitDefaultTimeout,
		"reset", "--quiet", "HEAD", "--pathspec-from-file=-", "--pathspec-file-nul")
	if runErr != nil {
		return nil, runErr
	}
	if err := gitEnsureSuccess(reset, "git reset selected paths"); err != nil {
		return nil, fmt.Errorf("已创建 Stash，但清理所选文件的暂存状态失败：%w", err)
	}
	return files, nil
}

func gitLiteralPathspecInput(paths []string) string {
	pathspecs := make([]string, 0, len(paths))
	for _, path := range paths {
		pathspecs = append(pathspecs, ":(literal)"+path)
	}
	return strings.Join(pathspecs, "\x00") + "\x00"
}

func gitSelectChangedFiles(status gitStatus, requested *[]string) ([]gitFileEntry, []string, error) {
	all := gitUniqueChangedFiles(status)
	if requested == nil {
		paths := make([]string, 0, len(all))
		for _, file := range all {
			paths = append(paths, file.Path)
		}
		return all, paths, nil
	}
	if len(*requested) == 0 {
		return nil, nil, fmt.Errorf("请至少选择一个文件")
	}

	byPath := make(map[string]gitFileEntry, len(all))
	for _, file := range all {
		byPath[file.Path] = file
	}
	files := make([]gitFileEntry, 0, len(*requested))
	paths := make([]string, 0, len(*requested)*2)
	seen := make(map[string]struct{}, len(*requested))
	seenPathspec := make(map[string]struct{}, len(*requested)*2)
	appendPathspec := func(path string) {
		if path == "" {
			return
		}
		if _, exists := seenPathspec[path]; exists {
			return
		}
		seenPathspec[path] = struct{}{}
		paths = append(paths, path)
	}
	for _, path := range *requested {
		if path == "" {
			return nil, nil, fmt.Errorf("文件路径不能为空")
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		file, ok := byPath[path]
		if !ok {
			return nil, nil, fmt.Errorf("文件不在当前更改中：%s", path)
		}
		seen[path] = struct{}{}
		files = append(files, file)
		appendPathspec(path)
		if file.OldPath != nil {
			appendPathspec(*file.OldPath)
		}
	}
	return files, paths, nil
}

func gitUniqueChangedFiles(status gitStatus) []gitFileEntry {
	groups := [][]gitFileEntry{status.Staged, status.Unstaged, status.Untracked}
	files := make([]gitFileEntry, 0, len(status.Staged)+len(status.Unstaged)+len(status.Untracked))
	indexes := make(map[string]int, cap(files))
	for _, group := range groups {
		for _, file := range group {
			if index, exists := indexes[file.Path]; exists {
				// Prefer the rename source when only one side of the staged/unstaged entry carries it.
				if files[index].OldPath == nil && file.OldPath != nil {
					files[index].OldPath = file.OldPath
				}
				continue
			}
			indexes[file.Path] = len(files)
			files = append(files, file)
		}
	}
	return files
}

func gitStashFiles(repoPath, spec string) []gitFileEntry {
	output, err := runGit(repoPath, gitDefaultTimeout,
		"stash", "show", "--name-status", "--format=", "--include-untracked", "-z", spec)
	if err != nil || !gitSucceeded(output) {
		return []gitFileEntry{}
	}
	return parseGitNameStatusZ(output.stdout)
}

func parseGitNameStatusZ(output []byte) []gitFileEntry {
	tokens := strings.Split(string(output), "\x00")
	files := make([]gitFileEntry, 0, len(tokens)/2)
	for index := 0; index < len(tokens); {
		status := tokens[index]
		index++
		if status == "" || index >= len(tokens) {
			continue
		}
		if status[0] == 'R' || status[0] == 'C' {
			if index+1 >= len(tokens) {
				break
			}
			oldPath := tokens[index]
			path := tokens[index+1]
			index += 2
			files = append(files, gitFileEntry{Path: path, Status: status, OldPath: &oldPath})
			continue
		}
		path := tokens[index]
		index++
		files = append(files, gitFileEntry{Path: path, Status: status})
	}
	return files
}
