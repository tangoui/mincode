package db

// Validation of untrusted plugin driver output (kindPlugin sessions).
//
// Plugin rows, notices and metadata are treated like remote database content:
// the host checks shapes and bounds, normalizes cells to the same JSON value
// space as the built-in normalizeValue, re-estimates the size with
// estimateDBValueBytes and enforces dbMaxResultRows/dbMaxResultBytes with the
// built-in truncation notice.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	externalColumnsMax        = 1024
	externalNameMaxBytes      = 1024
	externalCellDepthMax      = 16
	externalNoticeMaxRunes    = 1024
	externalPageTokenMaxBytes = 1024
	externalMetadataMaxBytes  = 8 * 1024 * 1024
	externalMetadataMaxObject = 50000
	externalMaxSafeInteger    = 1<<53 - 1
)

// errExternalStopPaging is returned from the page callback when the host has
// reached a cap; providers propagate it and close the cursor.
var errExternalStopPaging = errors.New("host stopped paging")

func externalProtocolError(format string, args ...any) error {
	return fmt.Errorf("protocol: "+format, args...)
}

func externalTruncationNotice() string {
	return fmt.Sprintf("查询结果已截断：最多返回 %d 行或 %d MiB，请添加 LIMIT 或缩小查询范围", dbMaxResultRows, dbMaxResultBytes/(1024*1024))
}

// sanitizeExternalText strips control characters and bounds the rune count.
func sanitizeExternalText(value string, maxRunes int) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	var b strings.Builder
	count := 0
	for _, r := range value {
		if count >= maxRunes {
			break
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			r = ' '
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

func validateExternalName(kind, name string, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(name) == "" {
		return externalProtocolError("插件返回的%s名称为空", kind)
	}
	if len(name) > externalNameMaxBytes || !utf8.ValidString(name) {
		return externalProtocolError("插件返回的%s名称无效或过长", kind)
	}
	return nil
}

func validateExternalColumns(columns []ExternalColumn) ([]string, error) {
	if len(columns) > externalColumnsMax {
		return nil, externalProtocolError("插件返回的列数超过 %d", externalColumnsMax)
	}
	out := make([]string, len(columns))
	for i, column := range columns {
		if err := validateExternalName("列", column.Name, true); err != nil {
			return nil, err
		}
		out[i] = column.Name
	}
	return out, nil
}

// normalizeExternalCell validates one cell and converts it into the value
// space of the built-in drivers: nil, bool, string, float64/int64, or a
// bounded object/array. Integers beyond ±2^53 become strings.
func normalizeExternalCell(value any, depth int) (any, error) {
	switch x := value.(type) {
	case nil, bool, string:
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, externalProtocolError("插件返回了非有限数值")
		}
		return x, nil
	case float32:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, externalProtocolError("插件返回了非有限数值")
		}
		return f, nil
	case int:
		return normalizeExternalInt(int64(x)), nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case int64:
		return normalizeExternalInt(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint:
		if uint64(x) > externalMaxSafeInteger {
			return strconv.FormatUint(uint64(x), 10), nil
		}
		return int64(x), nil
	case uint64:
		if x > externalMaxSafeInteger {
			return strconv.FormatUint(x, 10), nil
		}
		return int64(x), nil
	case json.Number:
		text := x.String()
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return normalizeExternalInt(n), nil
		}
		if strings.ContainsAny(text, ".eE") {
			f, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, externalProtocolError("插件返回了无效数值")
			}
			return f, nil
		}
		// An integer outside int64: keep the exact digits as a string.
		if !isDecimalInteger(text) {
			return nil, externalProtocolError("插件返回了无效数值")
		}
		return text, nil
	case map[string]any:
		if depth >= externalCellDepthMax {
			return nil, externalProtocolError("插件返回的单元格嵌套超过 %d 层", externalCellDepthMax)
		}
		out := make(map[string]any, len(x))
		for key, item := range x {
			normalized, err := normalizeExternalCell(item, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case []any:
		if depth >= externalCellDepthMax {
			return nil, externalProtocolError("插件返回的单元格嵌套超过 %d 层", externalCellDepthMax)
		}
		out := make([]any, len(x))
		for i, item := range x {
			normalized, err := normalizeExternalCell(item, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	default:
		return nil, externalProtocolError("插件返回了不支持的单元格类型 %T", value)
	}
}

// isDecimalInteger reports whether text is an optionally negative run of
// decimal digits (a JSON integer).
func isDecimalInteger(text string) bool {
	digits := strings.TrimPrefix(text, "-")
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizeExternalInt(n int64) any {
	if n > externalMaxSafeInteger || n < -externalMaxSafeInteger {
		return strconv.FormatInt(n, 10)
	}
	return n
}

// normalizeExternalRow validates one row and returns it with the same size
// estimate the built-in sqlRowScanner uses.
func normalizeExternalRow(raw []any, columnCount int) ([]any, int, error) {
	if len(raw) != columnCount {
		return nil, 0, externalProtocolError("插件返回的行有 %d 个单元格，但结果有 %d 列", len(raw), columnCount)
	}
	row := make([]any, len(raw))
	rowBytes := 32
	for i, cell := range raw {
		normalized, err := normalizeExternalCell(cell, 0)
		if err != nil {
			return nil, 0, err
		}
		row[i] = normalized
		rowBytes += estimateDBValueBytes(normalized) + 16
	}
	return row, rowBytes, nil
}

// externalPager consumes provider pages: it validates columns, rows and
// notices, enforces the host caps and hands accepted rows to onRow.
type externalPager struct {
	columns     []string
	haveColumns bool
	rowCount    int
	totalBytes  int
	// maxRows/maxBytes are the host result caps (truncation notice);
	// rowLimit silently discards rows beyond a requested page size.
	maxRows   int
	maxBytes  int
	rowLimit  int
	truncated bool
	stopped   bool
	notices   []string

	onColumns func([]string) error
	onRow     func(row []any, rowBytes int) error
}

func newExternalPager() *externalPager {
	return &externalPager{maxRows: dbMaxResultRows, maxBytes: dbMaxResultBytes}
}

func (p *externalPager) setColumns(columns []ExternalColumn) error {
	names, err := validateExternalColumns(columns)
	if err != nil {
		return err
	}
	if p.haveColumns {
		if len(names) != len(p.columns) {
			return externalProtocolError("插件在后续分页中改变了结果列")
		}
		for i := range names {
			if names[i] != p.columns[i] {
				return externalProtocolError("插件在后续分页中改变了结果列")
			}
		}
		return nil
	}
	p.columns = names
	p.haveColumns = true
	p.totalBytes = estimateColumnsBytes(names)
	if p.onColumns != nil {
		return p.onColumns(names)
	}
	return nil
}

func (p *externalPager) addNotice(notice string) {
	notice = sanitizeExternalText(notice, externalNoticeMaxRunes)
	if notice == "" || len(p.notices) >= 4 {
		return
	}
	for _, existing := range p.notices {
		if existing == notice {
			return
		}
	}
	p.notices = append(p.notices, notice)
}

func (p *externalPager) page(page ExternalPage) error {
	if p.stopped {
		return errExternalStopPaging
	}
	if len(page.Columns) > 0 {
		if err := p.setColumns(page.Columns); err != nil {
			return err
		}
	}
	if page.Notice != "" {
		p.addNotice(page.Notice)
	}
	if len(page.Rows) > 0 && !p.haveColumns {
		return externalProtocolError("插件在返回列之前返回了行")
	}
	for _, raw := range page.Rows {
		if p.rowLimit > 0 && p.rowCount >= p.rowLimit {
			p.stopped = true
			return errExternalStopPaging
		}
		row, rowBytes, err := normalizeExternalRow(raw, len(p.columns))
		if err != nil {
			return err
		}
		if p.rowCount >= p.maxRows || p.totalBytes+rowBytes > p.maxBytes {
			p.truncated = true
			p.stopped = true
			return errExternalStopPaging
		}
		if p.onRow != nil {
			if err := p.onRow(row, rowBytes); err != nil {
				if errors.Is(err, errExternalStopPaging) {
					p.stopped = true
				}
				return err
			}
		}
		p.totalBytes += rowBytes
		p.rowCount++
	}
	return nil
}

// finish applies the execution summary after the last page.
func (p *externalPager) finish(summary *ExternalExecuteSummary) error {
	if summary == nil {
		return nil
	}
	if !p.haveColumns && len(summary.Columns) > 0 {
		if err := p.setColumns(summary.Columns); err != nil {
			return err
		}
	}
	if summary.Notice != "" {
		p.addNotice(summary.Notice)
	}
	if summary.Truncated {
		p.truncated = true
	}
	return nil
}

func (p *externalPager) resultColumns() []string {
	if p.columns == nil {
		return []string{}
	}
	return p.columns
}

// notice combines the host truncation notice with the plugin notices.
func (p *externalPager) notice(extra ...string) *string {
	parts := make([]string, 0, len(p.notices)+2)
	if p.truncated {
		parts = append(parts, externalTruncationNotice())
	}
	for _, value := range extra {
		if value != "" {
			parts = append(parts, value)
		}
	}
	parts = append(parts, p.notices...)
	if len(parts) == 0 {
		return nil
	}
	joined := strings.Join(parts, "；")
	return &joined
}

func externalAffectedRows(summary *ExternalExecuteSummary) (*int64, error) {
	if summary == nil || summary.AffectedRows == nil {
		return nil, nil
	}
	if *summary.AffectedRows < 0 {
		return nil, externalProtocolError("插件返回了负的影响行数")
	}
	n := *summary.AffectedRows
	return &n, nil
}

func validateExternalPageToken(token string) (*string, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) > externalPageTokenMaxBytes || !utf8.ValidString(token) {
		return nil, externalProtocolError("插件返回的分页令牌无效或过长")
	}
	for _, r := range token {
		if unicode.IsControl(r) {
			return nil, externalProtocolError("插件返回的分页令牌无效或过长")
		}
	}
	return &token, nil
}

// ── metadata ──

type externalObjectBudget struct{ remaining int }

func newExternalObjectBudget() *externalObjectBudget {
	return &externalObjectBudget{remaining: externalMetadataMaxObject}
}

func (b *externalObjectBudget) take(n int) error {
	b.remaining -= n
	if b.remaining < 0 {
		return fmt.Errorf("too_large: 插件返回的元数据超过 %d 个对象", externalMetadataMaxObject)
	}
	return nil
}

func decodeExternalMetadata(kind ExternalMetadataKind, raw json.RawMessage, out any) error {
	if len(raw) > externalMetadataMaxBytes {
		return fmt.Errorf("too_large: 插件返回的元数据超过 %d MiB", externalMetadataMaxBytes/(1024*1024))
	}
	if len(raw) == 0 {
		return externalProtocolError("插件未返回 %s 元数据", kind)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return externalProtocolError("插件返回的 %s 元数据格式无效", kind)
	}
	return nil
}

func validateExternalSchemaTables(tables []SchemaTable, budget *externalObjectBudget) error {
	if err := budget.take(len(tables)); err != nil {
		return err
	}
	for _, table := range tables {
		if err := validateExternalName("表", table.Name, false); err != nil {
			return err
		}
		if len(table.Kind) > 64 {
			return externalProtocolError("插件返回的对象类型过长")
		}
	}
	return nil
}

func validateExternalSchemaInfo(info *DbSchemaInfo, budget *externalObjectBudget) error {
	if err := budget.take(len(info.Databases)); err != nil {
		return err
	}
	for _, database := range info.Databases {
		if err := validateExternalName("数据库", database.Name, true); err != nil {
			return err
		}
		if err := validateExternalSchemaTables(database.Tables, budget); err != nil {
			return err
		}
	}
	if info.Catalog != nil {
		if err := validateExternalName("目录", *info.Catalog, true); err != nil {
			return err
		}
	}
	return nil
}

func validateExternalColumnInfos(columns []ColumnInfo, budget *externalObjectBudget) error {
	if len(columns) > externalColumnsMax {
		return externalProtocolError("插件返回的列数超过 %d", externalColumnsMax)
	}
	if err := budget.take(len(columns)); err != nil {
		return err
	}
	for _, column := range columns {
		if err := validateExternalName("列", column.Name, false); err != nil {
			return err
		}
		if len(column.DataType) > externalNameMaxBytes {
			return externalProtocolError("插件返回的列类型过长")
		}
	}
	return nil
}

func validateExternalNameList(kind string, names []string, budget *externalObjectBudget) error {
	if err := budget.take(len(names)); err != nil {
		return err
	}
	for _, name := range names {
		if err := validateExternalName(kind, name, false); err != nil {
			return err
		}
	}
	return nil
}

func validateExternalIndexes(indexes []DbIndexInfo, budget *externalObjectBudget) error {
	if err := budget.take(len(indexes)); err != nil {
		return err
	}
	for _, index := range indexes {
		if err := validateExternalName("索引", index.Name, true); err != nil {
			return err
		}
		if err := validateExternalNameList("列", index.Columns, budget); err != nil {
			return err
		}
	}
	return nil
}

func validateExternalForeignKeys(keys []DbForeignKeyInfo, budget *externalObjectBudget) error {
	if err := budget.take(len(keys)); err != nil {
		return err
	}
	for _, key := range keys {
		if err := validateExternalName("外键", key.Name, true); err != nil {
			return err
		}
		if err := validateExternalName("表", key.ReferencedTable, false); err != nil {
			return err
		}
		if err := validateExternalNameList("列", key.Columns, budget); err != nil {
			return err
		}
		if err := validateExternalNameList("列", key.ReferencedColumns, budget); err != nil {
			return err
		}
	}
	return nil
}

func validateExternalTriggers(triggers []DbTriggerInfo, budget *externalObjectBudget) error {
	if err := budget.take(len(triggers)); err != nil {
		return err
	}
	for _, trigger := range triggers {
		if err := validateExternalName("触发器", trigger.Name, false); err != nil {
			return err
		}
		if len(trigger.Timing) > 64 || len(trigger.Event) > 256 {
			return externalProtocolError("插件返回的触发器描述过长")
		}
	}
	return nil
}
