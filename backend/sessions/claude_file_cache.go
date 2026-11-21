package sessions

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
)

// claudeFileCacheLimit 限制每个缓存的条目数；超出时先剔除已不存在的文件，仍超出则整体清空。
const claudeFileCacheLimit = 4096

const unknownFileSize = -1

type fileStamp struct {
	size            int64
	modTimeUnixNano int64
}

func statFileStamp(path string) (fileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStampFromInfo(info), true
}

func fileStampFromInfo(info os.FileInfo) fileStamp {
	return fileStamp{size: info.Size(), modTimeUnixNano: info.ModTime().UnixNano()}
}

type claudeFileCacheEntry[T any] struct {
	stamp fileStamp
	value T
}

// claudeFileCache 按 (path, size, mtime) 缓存单个转录文件的解析结果，
// 文件未变化时状态轮询不必重读、重解析。
type claudeFileCache[T any] struct {
	mu      sync.Mutex
	entries map[string]claudeFileCacheEntry[T]
	compute func(path string, sizeHint int64) T
}

func newClaudeFileCache[T any](compute func(path string) T) *claudeFileCache[T] {
	return newClaudeFileCacheWithSizeHint(func(path string, _ int64) T {
		return compute(path)
	})
}

func newClaudeFileCacheWithSizeHint[T any](compute func(path string, sizeHint int64) T) *claudeFileCache[T] {
	return &claudeFileCache[T]{entries: make(map[string]claudeFileCacheEntry[T]), compute: compute}
}

func (c *claudeFileCache[T]) get(path string) T {
	stamp, ok := statFileStamp(path)
	return c.getWithStamp(path, stamp, ok)
}

func (c *claudeFileCache[T]) getWithStamp(path string, stamp fileStamp, ok bool) T {
	if !ok {
		c.mu.Lock()
		delete(c.entries, path)
		c.mu.Unlock()
		return c.compute(path, unknownFileSize)
	}
	c.mu.Lock()
	entry, hit := c.entries[path]
	c.mu.Unlock()
	if hit && entry.stamp == stamp {
		return entry.value
	}
	value := c.compute(path, stamp.size)
	c.mu.Lock()
	if _, exists := c.entries[path]; !exists && len(c.entries) >= claudeFileCacheLimit {
		c.pruneLocked()
	}
	c.entries[path] = claudeFileCacheEntry[T]{stamp: stamp, value: value}
	c.mu.Unlock()
	return value
}

func (c *claudeFileCache[T]) pruneLocked() {
	for path := range c.entries {
		if _, err := os.Stat(path); err != nil {
			delete(c.entries, path)
		}
	}
	if len(c.entries) >= claudeFileCacheLimit {
		clear(c.entries)
	}
}

var (
	claudeCwdCache     = newClaudeFileCache(readSessionFileCwd)
	claudeStatusCache  = newClaudeFileCache(inferClaudeSessionStatus)
	claudeSummaryCache = newClaudeFileCacheWithSizeHint(readClaudeTranscriptSummary)
)

// readSessionFileCwd 只读文件开头 96KB，找到第一条非空 cwd 即停止。
func readSessionFileCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	reader := bufio.NewReader(io.LimitReader(f, 96*1024))
	for {
		line, readErr := reader.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var v map[string]json.RawMessage
			if json.Unmarshal([]byte(line), &v) == nil {
				if c := strings.TrimSpace(rawString(v, "cwd")); c != "" {
					return c
				}
			}
		}
		if readErr != nil {
			return ""
		}
	}
}

// claudeTranscriptSummary 是 get_project_sessions 从单个转录中提取的全部信息。
// Cwds 按出现顺序去重保存，调用方据此取第一个不同于目录名解码结果的 cwd。
type claudeTranscriptSummary struct {
	Cwds         []string
	FirstTitle   string
	Model        string
	LastPrompt   string
	MessageCount uint32
}

func readClaudeTranscriptSummary(path string, sizeHint int64) claudeTranscriptSummary {
	prefix, lastPrompt, messageCount := readJsonlPrefixAndCount(path, 96*1024, sizeHint)
	summary := claudeTranscriptSummary{LastPrompt: lastPrompt, MessageCount: messageCount}
	jsonLines(prefix, func(v map[string]json.RawMessage) {
		if cwd := strings.TrimSpace(rawString(v, "cwd")); cwd != "" && !slices.Contains(summary.Cwds, cwd) {
			summary.Cwds = append(summary.Cwds, cwd)
		}
		if summary.FirstTitle == "" {
			summary.FirstTitle = claudePromptTitleFromEntry(v)
		}
		if summary.Model == "" {
			if msgRaw, ok := v["message"]; ok {
				var msg map[string]json.RawMessage
				if json.Unmarshal(msgRaw, &msg) == nil {
					if m := rawString(msg, "model"); m != "" {
						summary.Model = m
					}
				}
			}
		}
	})
	return summary
}

// decodedPath 与原逐行逻辑一致：取第一个不同于 fallback 的 cwd，否则回退 fallback。
func (s claudeTranscriptSummary) decodedPath(fallback string) string {
	for _, cwd := range s.Cwds {
		if cwd != fallback {
			return cwd
		}
	}
	return fallback
}
