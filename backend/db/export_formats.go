package db

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// exportSQLBatchSize 控制批量追加模式下单条 INSERT 合并的最大行数，
// 避免超大单语句触发服务端包大小限制（如 MySQL max_allowed_packet）。
const exportSQLBatchSize = 500

// exportMarkdown 生成 Markdown 表格，单元格内竖线与换行做安全转义。
func exportMarkdown(r *QueryResult) string {
	var b strings.Builder
	writeMarkdownRow := func(cells []string) {
		b.WriteString("| ")
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString(" |\n")
	}
	header := make([]string, len(r.Columns))
	for i, col := range r.Columns {
		header[i] = markdownCell(col)
	}
	writeMarkdownRow(header)
	separator := make([]string, len(r.Columns))
	for i := range separator {
		separator[i] = "---"
	}
	writeMarkdownRow(separator)
	for _, row := range r.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = markdownCell(valueToCSVString(v))
		}
		writeMarkdownRow(cells)
	}
	return b.String()
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "|", `\|`)
}

// exportTXT 生成制表符分隔文本，单元格内的制表符与换行替换为空格。
func exportTXT(r *QueryResult) string {
	var b strings.Builder
	header := make([]string, len(r.Columns))
	for i, col := range r.Columns {
		header[i] = txtCell(col)
	}
	b.WriteString(strings.Join(header, "\t"))
	for _, row := range r.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = txtCell(valueToCSVString(v))
		}
		b.WriteString("\n")
		b.WriteString(strings.Join(cells, "\t"))
	}
	b.WriteString("\n")
	return b.String()
}

func txtCell(value string) string {
	for _, token := range []string{"\r\n", "\n", "\r", "\t"} {
		value = strings.ReplaceAll(value, token, " ")
	}
	return value
}

// xlsxCellMaxRunes 是 Excel 单元格的最大字符数（ECMA-376 限制 32767）。
const xlsxCellMaxRunes = 32767

// exportXLSX 生成 Excel 工作簿并以 base64 返回，由前端走二进制写盘落地。
func exportXLSX(r *QueryResult) (string, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	for i, col := range r.Columns {
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			return "", fmt.Errorf("导出 XLSX 失败: %w", err)
		}
		if err := f.SetCellValue(sheet, cell, truncateXLSXCell(col)); err != nil {
			return "", fmt.Errorf("导出 XLSX 失败: %w", err)
		}
	}
	for rowIndex, row := range r.Rows {
		for colIndex, v := range row {
			cell, err := excelize.CoordinatesToCellName(colIndex+1, rowIndex+2)
			if err != nil {
				return "", fmt.Errorf("导出 XLSX 失败: %w", err)
			}
			if err := f.SetCellValue(sheet, cell, xlsxCellValue(v)); err != nil {
				return "", fmt.Errorf("导出 XLSX 失败: %w", err)
			}
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return "", fmt.Errorf("导出 XLSX 失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// xlsxCellValue 将驱动返回值映射为 excelize 支持的单元格类型，
// 二进制与时间统一转为可读文本，避免方言相关的序列化差异。
func xlsxCellValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return truncateXLSXCell(x)
	case bool:
		return x
	case []byte:
		return truncateXLSXCell("0x" + strings.ToUpper(hex.EncodeToString(x)))
	case time.Time:
		if x.Nanosecond() == 0 {
			return x.Format("2006-01-02 15:04:05")
		}
		return x.Format("2006-01-02 15:04:05.999999")
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return x
	default:
		return truncateXLSXCell(fmt.Sprintf("%v", x))
	}
}

func truncateXLSXCell(value string) string {
	runes := []rune(value)
	if len(runes) <= xlsxCellMaxRunes {
		return value
	}
	return string(runes[:xlsxCellMaxRunes])
}

// exportInsertSQL 生成 INSERT 语句：batch 模式每 exportSQLBatchSize 行合并一条，
// rows 模式每行一条独立语句，便于单独执行和排查。
func exportInsertSQL(r *QueryResult, dialect, database, table, mode string) (string, error) {
	tbl, err := qualifiedTable(dialect, database, table)
	if err != nil {
		return "", fmt.Errorf("导出 SQL INSERT 失败: %w", err)
	}
	quoted := make([]string, len(r.Columns))
	for i, col := range r.Columns {
		quoted[i] = quoteColumn(dialect, col)
	}
	prefix := fmt.Sprintf("INSERT INTO %s (%s)", tbl, strings.Join(quoted, ", "))

	tuples := make([]string, len(r.Rows))
	for i, row := range r.Rows {
		values := make([]string, len(row))
		for j, v := range row {
			values[j] = sqlExportLiteral(dialect, v)
		}
		tuples[i] = "(" + strings.Join(values, ", ") + ")"
	}

	var b strings.Builder
	if strings.EqualFold(strings.TrimSpace(mode), "rows") {
		for _, tuple := range tuples {
			b.WriteString(prefix)
			b.WriteString("\nVALUES ")
			b.WriteString(tuple)
			b.WriteString(";\n")
		}
		return b.String(), nil
	}
	for start := 0; start < len(tuples); start += exportSQLBatchSize {
		end := start + exportSQLBatchSize
		if end > len(tuples) {
			end = len(tuples)
		}
		b.WriteString(prefix)
		b.WriteString("\nVALUES\n  ")
		b.WriteString(strings.Join(tuples[start:end], ",\n  "))
		b.WriteString(";\n")
	}
	return b.String(), nil
}

// sqlExportLiteral 将驱动返回值转为 SQL 字面量。导出按标准 SQL 尽力生成，
// 特殊类型（如 Oracle 时间字面量）不在此处做方言级展开。
func sqlExportLiteral(dialect string, v any) string {
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case string:
		return sqlExportQuotedString(dialect, x)
	case bool:
		switch dialect {
		case "sqlserver", "oracle", "dm", "yashandb", "firebird":
			if x {
				return "1"
			}
			return "0"
		default:
			if x {
				return "TRUE"
			}
			return "FALSE"
		}
	case []byte:
		hexText := strings.ToUpper(hex.EncodeToString(x))
		switch dialect {
		case "postgres":
			return fmt.Sprintf("decode('%s', 'hex')", hexText)
		case "sqlserver", "clickhouse":
			return "0x" + hexText
		default:
			return "X'" + hexText + "'"
		}
	case time.Time:
		if x.Nanosecond() == 0 {
			return sqlExportQuotedString(dialect, x.Format("2006-01-02 15:04:05"))
		}
		return sqlExportQuotedString(dialect, x.Format("2006-01-02 15:04:05.999999"))
	case float32, float64:
		return fmt.Sprintf("%v", x)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%v", x)
	default:
		return sqlExportQuotedString(dialect, fmt.Sprintf("%v", x))
	}
}

func sqlExportQuotedString(dialect, value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	// MySQL 系与 ClickHouse 默认启用反斜杠转义，需同步转义避免注入歧义。
	switch dialect {
	case "mysql", "clickhouse":
		escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	}
	return "'" + escaped + "'"
}
