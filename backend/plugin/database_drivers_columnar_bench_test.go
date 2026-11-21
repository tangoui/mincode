package plugin

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// benchDriverPages builds one 1000-row page of the driver benchmark shape
// (id, name, category, amount, created_at, 64-byte payload) as a protocol
// 1.x JSON page and as a columnar page, wrapped in the JSON-RPC envelope the
// runtime parses.
func benchDriverPages(tb testing.TB) (jsonLine, columnarLine []byte) {
	tb.Helper()
	const rowCount = 1000
	categories := []string{"alpha", "beta", "gamma", "delta"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := make([][]any, rowCount)
	for i := range rows {
		rows[i] = []any{i, "row-" + strconv.Itoa(i), categories[i%4], float64(i) / 7, start.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano), strings.Repeat("x", 64)}
	}
	columns := []map[string]string{{"name": "id"}, {"name": "name"}, {"name": "category"}, {"name": "amount"}, {"name": "created_at"}, {"name": "payload"}}
	page, _ := json.Marshal(map[string]any{"columns": columns, "rows": rows, "cursor": "c1", "done": false})
	jsonLine = []byte(`{"jsonrpc":"2.0","id":"7","result":` + string(page) + `}`)

	bin := []byte(databaseDriverColumnarMagic)
	bin = binary.AppendUvarint(bin, rowCount)
	bin = binary.AppendUvarint(bin, 6)
	bin = append(bin, columnarKindInt)
	for i := range rows {
		bin = binary.AppendUvarint(bin, uint64(i)<<1)
	}
	for _, column := range []int{1, 2, 3, 4, 5} {
		if column == 3 {
			bin = append(bin, columnarKindFloat)
			for _, row := range rows {
				bin = binary.LittleEndian.AppendUint64(bin, math.Float64bits(row[3].(float64)))
			}
			continue
		}
		bin = append(bin, columnarKindString)
		for _, row := range rows {
			bin = binary.AppendUvarint(bin, uint64(len(row[column].(string))))
		}
		for _, row := range rows {
			bin = append(bin, row[column].(string)...)
		}
	}
	columnsJSON, _ := json.Marshal(columns)
	columnarLine = []byte(`{"jsonrpc":"2.0","id":"7","result":{"columns":` + string(columnsJSON) + `,"encoding":"columnar1","data":"` + base64.StdEncoding.EncodeToString(bin) + `","cursor":"c1","done":false}}`)
	return jsonLine, columnarLine
}

// BenchmarkDatabaseDriverPageDecode measures the host side of one page:
// the runtime's envelope parse, the page header and the row decode.
func BenchmarkDatabaseDriverPageDecode(b *testing.B) {
	jsonLine, columnarLine := benchDriverPages(b)
	for _, test := range []struct {
		name     string
		line     []byte
		columnar bool
	}{{"json", jsonLine, false}, {"columnar", columnarLine, true}} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(test.line)))
			for b.Loop() {
				var message rpcEnvelope
				if err := json.Unmarshal(test.line, &message); err != nil {
					b.Fatal(err)
				}
				page, err := decodeDatabaseDriverPageHeaderEncoded(databaseDriverMethodExecute, message.Result, databaseDriverPageMaxBytesV11, true, test.columnar)
				if err != nil {
					b.Fatal(err)
				}
				if err := page.decodeRows(databaseDriverMethodExecute, 1000); err != nil || len(page.decodedRows) != 1000 {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestDatabaseDriverBenchPagesDecodeAlike(t *testing.T) {
	jsonLine, columnarLine := benchDriverPages(t)
	decode := func(line []byte, columnar bool) [][]any {
		var message rpcEnvelope
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatal(err)
		}
		page := decodeTranscriptPage(t, message.Result, databaseDriverMethodExecute, 1000, true, columnar)
		return page.decodedRows
	}
	want, got := decode(jsonLine, false), decode(columnarLine, true)
	for r := range want {
		for c := range want[r] {
			wantCell := want[r][c]
			if number, ok := wantCell.(json.Number); ok {
				if n, err := number.Int64(); err == nil {
					wantCell = n
				} else {
					wantCell, _ = number.Float64()
				}
			}
			if wantCell != got[r][c] {
				t.Fatalf("row %d column %d: columnar %#v, JSON %#v", r, c, got[r][c], want[r][c])
			}
		}
	}
}
