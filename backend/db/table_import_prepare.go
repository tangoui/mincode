package db

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func prepareImportRows(ctx context.Context, input io.Reader, format string, columns []string, root string, limits transferLimits) (string, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	stagingMu.Lock()
	defer stagingMu.Unlock()
	used, err := transferStagingBytes(root)
	if err != nil {
		return "", err
	}
	if used >= limits.ExpandedBytes {
		return "", fmt.Errorf("transfer_staging_limit")
	}
	limits.ExpandedBytes -= used
	f, err := os.CreateTemp(root, "import-*.jsonl")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(f.Name())
		}
	}()
	allowed := map[string]bool{}
	for _, c := range columns {
		allowed[c] = true
	}
	output := bufio.NewWriter(f)
	encoder := json.NewEncoder(output)
	source := &transferReader{ctx: ctx, r: input, left: limits.SourceBytes}
	bounded := &importRecordReader{input: bufio.NewReader(source), limit: limits.RecordBytes, json: strings.EqualFold(format, "json")}
	total := int64(0)
	write := func(record map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(record) == 0 {
			return fmt.Errorf("import_empty_record")
		}
		for key, value := range record {
			if !allowed[key] {
				return fmt.Errorf("import_unknown_column")
			}
			switch value.(type) {
			case nil, string, bool, json.Number:
			default:
				return fmt.Errorf("import_non_scalar_value")
			}
		}
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(raw) > limits.RecordBytes {
			return fmt.Errorf("transfer_record_limit")
		}
		total += int64(len(raw) + 1)
		if total > limits.ExpandedBytes {
			return fmt.Errorf("transfer_size_limit")
		}
		return encoder.Encode(record)
	}
	switch strings.ToLower(format) {
	case "csv":
		reader := csv.NewReader(bounded)
		header, err := reader.Read()
		if err != nil {
			return "", fmt.Errorf("import_csv_header_invalid")
		}
		seen := map[string]bool{}
		for _, name := range header {
			if name == "" || seen[name] || !allowed[name] {
				return "", fmt.Errorf("import_csv_header_invalid")
			}
			seen[name] = true
		}
		for {
			row, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", fmt.Errorf("import_csv_row_invalid")
			}
			record := map[string]any{}
			for i, col := range header {
				record[col] = row[i]
			}
			if err := write(record); err != nil {
				return "", err
			}
		}
	case "json":
		decoder := json.NewDecoder(bounded)
		decoder.UseNumber()
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return "", fmt.Errorf("import_json_array_required")
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return "", fmt.Errorf("import_json_record_invalid")
			}
			if len(raw) > limits.RecordBytes {
				return "", fmt.Errorf("transfer_record_limit")
			}
			var record map[string]any
			if err := strictTransferJSON(raw, &record); err != nil {
				return "", err
			}
			if err := write(record); err != nil {
				return "", err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return "", fmt.Errorf("import_json_array_invalid")
		}
		if decoder.Decode(new(any)) != io.EOF {
			return "", fmt.Errorf("import_json_trailing_data")
		}
	default:
		return "", fmt.Errorf("import_format_unsupported")
	}
	if err := output.Flush(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	ok = true
	return f.Name(), nil
}
func importInput(path, data string) (io.ReadCloser, error) {
	if path == "" {
		if len(data) > 1<<20 {
			return nil, fmt.Errorf("import_inline_size_limit")
		}
		return io.NopCloser(bytes.NewBufferString(data)), nil
	}
	if data != "" || len(path) > 4096 {
		return nil, fmt.Errorf("import_source_invalid")
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("import_file_unavailable")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("import_file_not_regular")
	}
	return f, nil
}

// Bound encoded records before csv/json decoders allocate their full contents.
// Decoder validation remains authoritative; this only frames allocation limits.
type importRecordReader struct {
	input   *bufio.Reader
	limit   int
	size    int
	json    bool
	quoted  bool
	escaped bool
	depth   int
}

func (r *importRecordReader) Read(p []byte) (int, error) {
	for i := range p {
		b, err := r.input.ReadByte()
		if err != nil {
			return i, err
		}
		r.size++
		if r.size > r.limit {
			return i, fmt.Errorf("transfer_record_limit")
		}
		p[i] = b
		if r.json {
			if r.quoted {
				if r.escaped {
					r.escaped = false
				} else if b == '\\' {
					r.escaped = true
				} else if b == '"' {
					r.quoted = false
				}
			} else {
				switch b {
				case '"':
					r.quoted = true
				case '[', '{':
					r.depth++
				case ']', '}':
					r.depth--
				}
				if r.depth == 1 && (b == ',' || b == '[') {
					r.size = 0
				}
			}
		} else {
			if b == '"' {
				r.quoted = !r.quoted
			}
			if b == '\n' && !r.quoted {
				r.size = 0
			}
		}
	}
	return len(p), nil
}
