package db

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

const (
	sqliteHeader = "SQLite format 3\x00"
	duckDBMagic  = "DUCK"
)

type fileDatabaseInfo struct {
	DbType   string `json:"dbType"`
	FilePath string `json:"filePath"`
	FileName string `json:"fileName"`
	// BuiltinDriver reports whether this build can open the file with its
	// built-in driver. It is false for DuckDB in the lean (nocoredrivers) and
	// Lite builds; the frontend then offers the duckdb-driver plugin instead
	// of treating the file as unsupported or falling back silently.
	BuiltinDriver bool `json:"builtinDriver"`
}

type detectFileDatabaseArgs struct {
	FilePath string `json:"filePath"`
}

func init() {
	core.Register("db_detect_file_type", dbDetectFileTypeHandler)
}

func dbDetectFileTypeHandler(_, argsJSON string) (string, error) {
	var args detectFileDatabaseArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	info, err := detectFileDatabase(args.FilePath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(info)
}

// detectFileDatabase 根据数据库文件头识别类型。不能只依赖扩展名，因为 SQLite
// 与 DuckDB 都常使用 .db，按内容识别可以避免用错误驱动打开文件。
func detectFileDatabase(path string) (*fileDatabaseInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("数据库文件路径不能为空")
	}

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析数据库文件路径失败: %w", err)
	}
	absolutePath = filepath.Clean(absolutePath)
	stat, err := os.Stat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("读取数据库文件失败: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("请选择 SQLite 或 DuckDB 数据库文件")
	}

	file, err := os.Open(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库文件失败: %w", err)
	}
	defer file.Close()

	header := make([]byte, 16)
	read, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("读取数据库文件头失败: %w", err)
	}
	header = header[:read]

	dbType := ""
	switch {
	case bytes.HasPrefix(header, []byte(sqliteHeader)):
		dbType = "sqlite"
	case len(header) >= 12 && string(header[8:12]) == duckDBMagic:
		dbType = "duckdb"
	default:
		return nil, fmt.Errorf("无法识别该文件，仅支持有效的 SQLite 或 DuckDB 数据库")
	}

	return &fileDatabaseInfo{
		DbType:        dbType,
		FilePath:      absolutePath,
		FileName:      filepath.Base(absolutePath),
		BuiltinDriver: databaseTypeSupported(dbType),
	}, nil
}

func databaseTypeSupported(dbType string) bool {
	for _, supported := range supportedDatabaseTypes {
		if supported == dbType {
			return true
		}
	}
	return false
}
