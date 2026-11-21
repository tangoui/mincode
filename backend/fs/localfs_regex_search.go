package fs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// local_fs_regex_search 的两个引擎返回同一份结果：
//   - ripgrep 只负责并行读盘、按行定位候选行；每个候选行再用 Go 正则重新匹配，
//     所以 matchStart/matchEnd、col、大小写与 \b 语义都与纯 Go 实现逐字节一致。
//     交给 rg 的正则是从 Go 语法树翻译出的"宽松超集"，只会多报候选行，不会漏行。
//   - 文件范围与纯 Go 遍历一致：包含隐藏文件、不读 .gitignore（--no-ignore）、
//     跳过 searchSkipDirNames、不跟随符号链接、> 2MB 跳过、前 512 字节判定二进制、
//     includeGlob 只按文件名匹配（* 与 ?）。
//   - 输出顺序相同（同目录按名称排序的深度优先）。rg 并行搜索、结果再排序；
//     命中超过 regexMaxResults 时 rg 保留的是先到达的那部分，子集不保证与 Go 相同。
// rg 不可用、模式无法翻译、或 rg 异常退出时回落到 Go 实现。

func handleRegexSearch(callCtx context.Context, _ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot   string  `json:"projectRoot"`
		Pattern       string  `json:"pattern"`
		CaseSensitive bool    `json:"caseSensitive"`
		WholeWord     bool    `json:"wholeWord"`
		IncludeGlob   *string `json:"includeGlob"`
		// 可选：只搜索项目内的这个子目录；结果路径仍相对项目根。
		ScopePath string `json:"scopePath"`
		// 可选：与 local_fs_cancel_code_search 共用取消通道。
		RequestID string `json:"requestId"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return core.EncodeResult([]regexMatch{})
	}
	re, err := buildSearchRegex(a.Pattern, a.CaseSensitive, a.WholeWord)
	if err != nil {
		return "", err
	}
	glob := globFilter(a.IncludeGlob)
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	scope, err := searchScopeUnderRoot(root, a.ScopePath)
	if err != nil {
		return "", err
	}
	ctx, cancel := registerCodeSearch(callCtx, a.RequestID)
	defer cancel()
	results, err := regexSearch(ctx, root, scope, re, glob)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(results)
}

func regexSearch(ctx context.Context, root, scope string, re *regexp.Regexp, glob string) ([]regexMatch, error) {
	// includeGlob 只与文件名比较，文件名不含 "/"，带 "/" 的 glob 必然一个都不匹配。
	if strings.ContainsRune(glob, '/') {
		return []regexMatch{}, nil
	}
	if results, ok := regexSearchWithRipgrep(ctx, root, scope, re, glob); ok {
		return results, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return regexSearchBuiltin(ctx, root, scope, re, glob)
}

// searchScopeUnderRoot resolves an optional project-relative directory to search, as a slash path
// relative to root ("." for the whole project). It must exist, be a directory and stay inside root.
func searchScopeUnderRoot(root, scopePath string) (string, error) {
	scopePath = strings.Trim(strings.TrimSpace(filepath.ToSlash(scopePath)), "/")
	if scopePath == "" || scopePath == "." {
		return ".", nil
	}
	resolved, err := resolveUnderRoot(root, scopePath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(resolved); statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("search_scope_not_dir")
	}
	return relPathPosix(root, resolved), nil
}

func regexSearchBuiltin(ctx context.Context, root, scope string, re *regexp.Regexp, glob string) ([]regexMatch, error) {
	prefilter := regexFilePrefilter(re)
	results := make([]regexMatch, 0, 64)
	var walk func(dir string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return nil
		}
		for _, e := range entries {
			if len(results) >= regexMaxResults {
				return nil
			}
			name := e.Name()
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			full := filepath.Join(dir, name)
			if e.IsDir() {
				if !shouldSkipSearchDir(name) {
					if err := walk(full); err != nil {
						return err
					}
				}
				continue
			}
			if !e.Type().IsRegular() || (glob != "" && !simpleGlobMatch(glob, name)) {
				continue
			}
			info, infoErr := e.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Size() > regexMaxFileBytes {
				continue
			}
			data, dataErr := os.ReadFile(full)
			if dataErr != nil {
				continue
			}
			if sampleLooksLikeBinaryForPreview(peekHead(data, regexBinaryPeekSize)) {
				continue
			}
			firstLine, lineStart := 1, 0
			if prefilter != nil {
				loc := prefilter.FindIndex(data)
				if loc == nil {
					continue
				}
				// 最左的整文件匹配之前不可能有逐行匹配，直接从它所在的行开始扫描。
				lineStart = bytes.LastIndexByte(data[:loc[0]], '\n') + 1
				firstLine += bytes.Count(data[:lineStart], []byte{'\n'})
			}
			results = appendRegexFileMatches(results, re, relPathPosix(root, full), full, string(data), lineStart, firstLine)
		}
		return nil
	}
	if err := walk(filepath.Join(root, filepath.FromSlash(scope))); err != nil {
		return nil, err
	}
	return results, nil
}

// appendRegexFileMatches 逐行匹配（行不含 "\n"，保留 "\r"），不预先切分整个文件。
func appendRegexFileMatches(results []regexMatch, re *regexp.Regexp, rel, full, text string, start, lineNumber int) []regexMatch {
	for len(results) < regexMaxResults {
		end := strings.IndexByte(text[start:], '\n')
		line := text[start:]
		if end >= 0 {
			line = text[start : start+end]
		}
		results = appendRegexLineMatches(results, re, rel, full, lineNumber, line)
		if end < 0 {
			break
		}
		start += end + 1
		lineNumber++
	}
	return results
}

func appendRegexLineMatches(results []regexMatch, re *regexp.Regexp, rel, full string, lineNumber int, line string) []regexMatch {
	remaining := regexMaxResults - len(results)
	if remaining <= 0 {
		return results
	}
	locs := re.FindAllStringIndex(line, remaining)
	if len(locs) == 0 {
		return results
	}
	lineText := truncateRunes(line, 300)
	for _, loc := range locs {
		results = append(results, regexMatch{
			RelativePath: rel, Path: full,
			Line: lineNumber, Col: loc[0] + 1,
			LineText:   lineText,
			MatchStart: loc[0], MatchEnd: loc[1],
		})
	}
	return results
}

// regexFilePrefilter 返回一个整文件级的快速判定：若任意一行能匹配 re，则它在整个
// 文件上必然匹配。做法是把 ^/$ 换成行锚点 (?m)；显式的 \A、\z 无法等价换算，返回 nil。
func regexFilePrefilter(re *regexp.Regexp) *regexp.Regexp {
	source := "(?m)" + re.String()
	tree, err := syntax.Parse(source, syntax.Perl)
	if err != nil || regexUsesTextAnchors(tree) {
		return nil
	}
	prefilter, err := regexp.Compile(source)
	if err != nil {
		return nil
	}
	return prefilter
}

func regexUsesTextAnchors(re *syntax.Regexp) bool {
	if re.Op == syntax.OpBeginText || re.Op == syntax.OpEndText {
		return true
	}
	for _, sub := range re.Sub {
		if regexUsesTextAnchors(sub) {
			return true
		}
	}
	return false
}

// ---------- ripgrep 引擎 ----------

type ripgrepData struct {
	Text  *string `json:"text"`
	Bytes string  `json:"bytes"`
}

func (d ripgrepData) value() (string, bool) {
	if d.Text != nil {
		return *d.Text, true
	}
	if d.Bytes == "" {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(d.Bytes)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

type ripgrepRegexEvent struct {
	Type string `json:"type"`
	Data struct {
		Path       ripgrepData `json:"path"`
		Lines      ripgrepData `json:"lines"`
		LineNumber int         `json:"line_number"`
	} `json:"data"`
}

func regexSearchWithRipgrep(ctx context.Context, root, scope string, re *regexp.Regexp, glob string) ([]regexMatch, bool) {
	// 能匹配空串的模式在每一行（含文件末尾换行后的空行）都命中，rg 与逐行切分对
	// 末尾空行的处理不同；这类模式很快就会触达结果上限，交给 Go 实现即可。
	if re.MatchString("") {
		return nil, false
	}
	rgPattern, ok := ripgrepPatternFromGo(re.String())
	if !ok {
		return nil, false
	}
	rgPath, err := findCodeSearchExecutable("rg")
	if err != nil {
		return nil, false
	}
	args := []string{
		"--json", "--no-config", "--no-messages", "--hidden", "--no-ignore",
		// 二进制判定与 BOM 处理由下方按 Go 规则完成，rg 这里按原始字节逐行搜索。
		"--text", "--encoding", "none", "--case-sensitive",
		"--max-filesize", strconv.Itoa(regexMaxFileBytes),
	}
	// rg 的 glob 后出现者优先：先放 include，再放目录排除，排除才能生效。
	if ripgrepCanPrefilterGlob(glob) {
		args = append(args, "--glob", glob)
	}
	for _, name := range searchSkipDirNames {
		args = append(args, "--glob", "!"+name+"/")
	}
	args = append(args, "--regexp", rgPattern, scope)

	command := exec.CommandContext(ctx, rgPath, args...)
	core.PrepareBackgroundCommand(command)
	command.Dir = root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := command.Start(); err != nil {
		return nil, false
	}

	results := make([]regexMatch, 0, 64)
	binaryByPath := make(map[string]bool)
	sawSummary := false
	decoder := json.NewDecoder(bufio.NewReaderSize(stdout, 64<<10))
	for len(results) < regexMaxResults {
		var event ripgrepRegexEvent
		if err := decoder.Decode(&event); err != nil {
			break
		}
		if event.Type == "summary" {
			sawSummary = true
			continue
		}
		if event.Type != "match" {
			continue
		}
		rawPath, ok := event.Data.Path.value()
		if !ok {
			continue
		}
		rel := path.Clean(filepath.ToSlash(rawPath))
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
			continue
		}
		if glob != "" && !simpleGlobMatch(glob, path.Base(rel)) {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		isBinary, seen := binaryByPath[rel]
		if !seen {
			peek, peekErr := readPeek(full, regexBinaryPeekSize)
			isBinary = peekErr != nil || sampleLooksLikeBinaryForPreview(peek)
			binaryByPath[rel] = isBinary
		}
		if isBinary {
			continue
		}
		line, ok := event.Data.Lines.value()
		if !ok {
			continue
		}
		line = strings.TrimSuffix(line, "\n")
		results = appendRegexLineMatches(results, re, rel, full, event.Data.LineNumber, line)
	}
	killed := len(results) >= regexMaxResults
	if killed {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
	if ctx.Err() != nil {
		return nil, false
	}
	// rg 在正则无法编译等致命错误时不会输出 summary；读目录失败之类的非致命错误
	// 虽然退出码为 2，仍会输出 summary，结果可用。
	if !killed && !sawSummary {
		return nil, false
	}
	sortRegexMatches(results)
	return results, true
}

// sortRegexMatches 把 rg 并行输出排成与 Go 遍历相同的顺序：按路径逐段字节序
// （同目录下按名称排序的深度优先），再按行号；同一行内的匹配已按列有序。
func sortRegexMatches(results []regexMatch) {
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.RelativePath != b.RelativePath {
			return comparePathSegments(a.RelativePath, b.RelativePath) < 0
		}
		return a.Line < b.Line
	})
}

func comparePathSegments(a, b string) int {
	for {
		aSeg, aRest, aMore := strings.Cut(a, "/")
		bSeg, bRest, bMore := strings.Cut(b, "/")
		if c := strings.Compare(aSeg, bSeg); c != 0 {
			return c
		}
		if !aMore || !bMore {
			switch {
			case aMore == bMore:
				return 0
			case aMore:
				return 1
			default:
				return -1
			}
		}
		a, b = aRest, bRest
	}
}

// ripgrepCanPrefilterGlob 只把与 simpleGlobMatch 语义相同的 glob 交给 rg 预筛；
// 其余（字符类、花括号、转义、注释前缀等）仍由逐条结果的 simpleGlobMatch 过滤。
func ripgrepCanPrefilterGlob(glob string) bool {
	return glob != "" && !strings.ContainsAny(glob, "[]{}\\!#,") && !strings.Contains(glob, "**")
}

const ripgrepAnyHighByte = `(?-u:[\x80-\xFF])`

// ripgrepPatternFromGo 把 Go 正则翻译成 rg 可用、且候选行集合是 Go 逐行匹配超集的正则：
//   - \b、\B 固定为 ASCII 语义（与 Go 一致，rg 默认是 Unicode 语义）；
//   - Go 把非法 UTF-8 字节当作 U+FFFD，凡能匹配 U+FFFD 的位置额外放行任意高位字节；
//   - ^/$/\A/\z 在单行内等价，统一译为行锚点；字符类去掉 "\n"（行内不会出现）。
//
// 翻译不了（如字面量 "\n"）时返回 false，由调用方回落到 Go 实现。
func ripgrepPatternFromGo(expr string) (string, bool) {
	tree, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	if !writeRipgrepRegex(&b, tree) {
		return "", false
	}
	return b.String(), true
}

func writeRipgrepRegex(b *strings.Builder, re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		b.WriteString("(?:)")
	case syntax.OpLiteral:
		b.WriteString("(?:")
		if re.Flags&syntax.FoldCase != 0 {
			b.WriteString("(?i)")
		}
		for _, r := range re.Rune {
			switch {
			case r == '\n' || (r >= 0xD800 && r <= 0xDFFF) || r > utf8.MaxRune:
				return false
			case r == utf8.RuneError:
				b.WriteString(`(?:\x{FFFD}|` + ripgrepAnyHighByte + `)`)
			default:
				fmt.Fprintf(b, `\x{%X}`, r)
			}
		}
		b.WriteString(")")
	case syntax.OpCharClass:
		return writeRipgrepClass(b, re.Rune)
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		b.WriteString(`(?:[^\n]|` + ripgrepAnyHighByte + `)`)
	case syntax.OpBeginLine, syntax.OpBeginText:
		b.WriteString("(?m:^)")
	case syntax.OpEndLine, syntax.OpEndText:
		b.WriteString("(?m:$)")
	case syntax.OpWordBoundary:
		b.WriteString(`(?-u:\b)`)
	case syntax.OpNoWordBoundary:
		b.WriteString(`(?-u:\B)`)
	case syntax.OpCapture:
		return writeRipgrepGroup(b, re.Sub[0], "")
	case syntax.OpStar:
		return writeRipgrepGroup(b, re.Sub[0], "*")
	case syntax.OpPlus:
		return writeRipgrepGroup(b, re.Sub[0], "+")
	case syntax.OpQuest:
		return writeRipgrepGroup(b, re.Sub[0], "?")
	case syntax.OpRepeat:
		suffix := fmt.Sprintf("{%d,%d}", re.Min, re.Max)
		if re.Max < 0 {
			suffix = fmt.Sprintf("{%d,}", re.Min)
		}
		return writeRipgrepGroup(b, re.Sub[0], suffix)
	case syntax.OpConcat, syntax.OpAlternate:
		b.WriteString("(?:")
		for i, sub := range re.Sub {
			if i > 0 && re.Op == syntax.OpAlternate {
				b.WriteString("|")
			}
			if !writeRipgrepRegex(b, sub) {
				return false
			}
		}
		b.WriteString(")")
	default:
		return false
	}
	return true
}

func writeRipgrepGroup(b *strings.Builder, sub *syntax.Regexp, suffix string) bool {
	b.WriteString("(?:")
	if !writeRipgrepRegex(b, sub) {
		return false
	}
	b.WriteString(")")
	b.WriteString(suffix)
	return true
}

func writeRipgrepClass(b *strings.Builder, ranges []rune) bool {
	var body strings.Builder
	matchesInvalid := false
	appendRange := func(lo, hi rune) {
		if lo >= 0xD800 && lo <= 0xDFFF {
			lo = 0xE000
		}
		if hi >= 0xD800 && hi <= 0xDFFF {
			hi = 0xD7FF
		}
		if hi > utf8.MaxRune {
			hi = utf8.MaxRune
		}
		if lo > hi {
			return
		}
		fmt.Fprintf(&body, `\x{%X}-\x{%X}`, lo, hi)
	}
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		if lo <= utf8.RuneError && utf8.RuneError <= hi {
			matchesInvalid = true
		}
		if lo <= '\n' && '\n' <= hi {
			if lo < '\n' {
				appendRange(lo, '\n'-1)
			}
			if hi > '\n' {
				appendRange('\n'+1, hi)
			}
			continue
		}
		appendRange(lo, hi)
	}
	if body.Len() == 0 && !matchesInvalid {
		return false
	}
	b.WriteString("(?:")
	if body.Len() > 0 {
		b.WriteString("[")
		b.WriteString(body.String())
		b.WriteString("]")
		if matchesInvalid {
			b.WriteString("|")
		}
	}
	if matchesInvalid {
		b.WriteString(ripgrepAnyHighByte)
	}
	b.WriteString(")")
	return true
}
