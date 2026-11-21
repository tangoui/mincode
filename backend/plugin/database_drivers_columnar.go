package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"slices"
	"strconv"
	"unicode/utf8"
)

// Experimental columnar page encoding (protocol v2 draft, "columnar1";
// docs/architecture/database-driver-protocol.md §12). It is used only when
// all of these hold: the host switch is on (MINCODE_DB_DRIVER_COLUMNAR=1),
// the plugin announced the encoding in its initialize details, the host
// requested it in plugin.db.connect and the plugin confirmed it in the
// connect info. Otherwise every page is a protocol 1.x JSON page. Grants,
// generation pinning, budgets, line caps and cell validation in backend/db
// are the same for both encodings; this file only turns the untrusted
// column blocks back into the [][]any rows the JSON path produces.

const (
	databaseDriverEncodingColumnar = "columnar1"
	databaseDriverEncodingsMax     = 16
	databaseDriverColumnarEnv      = "MINCODE_DB_DRIVER_COLUMNAR"
	databaseDriverColumnarMagic    = "MCC1"
	databaseDriverColumnsMax       = 1024
)

// databaseDriverColumnarEnabled is the explicit host switch (default off);
// tests override it.
var databaseDriverColumnarEnabled = func() bool { return os.Getenv(databaseDriverColumnarEnv) == "1" }

// databaseDriverRequestedEncoding returns the page encoding the host
// requests in plugin.db.connect: columnar1 when the switch is on and the
// generation's initialize details announce it, otherwise "" (JSON rows,
// and no "encoding" member, so pre-v2 plugins see unchanged params).
func databaseDriverRequestedEncoding(details json.RawMessage, enabled bool) string {
	if !enabled {
		return ""
	}
	var negotiation databaseDriverNegotiation
	if json.Unmarshal(details, &negotiation) != nil || negotiation.Protocol != databaseDriverProtocolVersion ||
		len(negotiation.Encodings) > databaseDriverEncodingsMax ||
		!slices.Contains(negotiation.Encodings, databaseDriverEncodingColumnar) {
		return ""
	}
	return databaseDriverEncodingColumnar
}

// databaseDriverAcceptedEncoding checks the connect info encoding against
// the request: a plugin may decline (empty: JSON rows), but never confirm an
// encoding the host did not request.
func databaseDriverAcceptedEncoding(requested, confirmed string) (bool, error) {
	switch {
	case confirmed == "":
		return false, nil
	case confirmed == requested:
		return true, nil
	}
	return false, databaseDriverProtocolError("plugin.db.connect confirmed an encoding the host did not request")
}

// Column kinds and mixed-cell tags (dbdriver/columnar.go).
const (
	columnarKindNull   = 0
	columnarKindBool   = 1
	columnarKindInt    = 2
	columnarKindFloat  = 3
	columnarKindString = 4
	columnarKindJSON   = 5
	columnarKindMixed  = 6
	columnarHasNulls   = 0x80

	columnarTagNull   = 0
	columnarTagFalse  = 1
	columnarTagTrue   = 2
	columnarTagInt    = 3
	columnarTagFloat  = 4
	columnarTagString = 5
	columnarTagJSON   = 6
)

type columnarDecoder struct {
	b      []byte
	i      int
	failed bool
}

func (d *columnarDecoder) fail() { d.failed = true }

func (d *columnarDecoder) byte() byte {
	if d.failed || d.i >= len(d.b) {
		d.fail()
		return 0
	}
	d.i++
	return d.b[d.i-1]
}

func (d *columnarDecoder) take(n uint64) []byte {
	if d.failed || n > uint64(len(d.b)-d.i) {
		d.fail()
		return nil
	}
	start := d.i
	d.i += int(n)
	return d.b[start:d.i]
}

func (d *columnarDecoder) uvarint() uint64 {
	if d.failed {
		return 0
	}
	v, n := binary.Uvarint(d.b[d.i:])
	if n <= 0 {
		d.fail()
		return 0
	}
	d.i += n
	return v
}

func (d *columnarDecoder) int() int64 {
	u := d.uvarint()
	return int64(u>>1) ^ -int64(u&1)
}

// float decodes a float cell into the value the JSON row path yields for
// the same float: the SDK writes an integral float below 1e21 without a
// fraction ("7"), which backend/db reads as an integer (int64, or the exact
// digits as a string beyond ±(2^53-1)).
func (d *columnarDecoder) float() any {
	raw := d.take(8)
	if raw == nil {
		return nil
	}
	f := math.Float64frombits(binary.LittleEndian.Uint64(raw))
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		if math.Abs(f) <= 1<<53-1 {
			return int64(f)
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return f
}

func (d *columnarDecoder) bool() bool {
	switch d.byte() {
	case 0:
		return false
	case 1:
		return true
	}
	d.fail()
	return false
}

// texts reads the lengths and bytes of a string/json column's present
// cells. It returns the block as one string (one allocation) and the cell
// offsets; string cells must be valid UTF-8 and every cell must start on a
// rune boundary, so each substring is valid UTF-8 too.
func (d *columnarDecoder) texts(present int, offsets []int) (string, []int) {
	offsets = offsets[:0]
	total := uint64(0)
	for range present {
		n := d.uvarint()
		if d.failed || n > uint64(len(d.b)) {
			d.fail()
			return "", offsets
		}
		offsets = append(offsets, int(total))
		total += n
	}
	block := d.take(total)
	if d.failed || !utf8.Valid(block) {
		d.fail()
		return "", offsets
	}
	for _, offset := range offsets {
		if offset < len(block) && !utf8.RuneStart(block[offset]) {
			d.fail()
			return "", offsets
		}
	}
	return string(block), append(offsets, len(block))
}

// databaseDriverJSONCell decodes one nested object/array cell like the JSON
// row path does (UseNumber; backend/db normalizes and bounds it).
func databaseDriverJSONCell(text string) (any, bool) {
	if text == "" || (text[0] != '{' && text[0] != '[') {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.More() {
		return nil, false
	}
	return value, true
}

// decodeDatabaseDriverColumnar decodes the base64 data member of a columnar
// page into rows. Cells are nil, bool, int64, float64, string or decoded
// JSON, the value space backend/db's normalizeExternalCell validates for
// JSON rows. maxRows is the requested page budget; the cell count is
// bounded by maxBytes (a JSON row needs at least one byte per cell), so a
// tiny page can never expand into a huge allocation.
func decodeDatabaseDriverColumnar(method string, data []byte, maxRows int, maxBytes int64) ([][]any, error) {
	malformed := func() ([][]any, error) {
		return nil, databaseDriverProtocolError("%s columnar page is malformed", method)
	}
	bin := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	n, err := base64.StdEncoding.Strict().Decode(bin, data)
	if err != nil {
		return malformed()
	}
	d := &columnarDecoder{b: bin[:n]}
	if string(d.take(uint64(len(databaseDriverColumnarMagic)))) != databaseDriverColumnarMagic {
		return malformed()
	}
	rowCount, columnCount := d.uvarint(), d.uvarint()
	if d.failed || columnCount > databaseDriverColumnsMax {
		return malformed()
	}
	if rowCount > uint64(maxRows) {
		return nil, databaseDriverProtocolError("%s page has %d rows, more than the requested %d", method, rowCount, maxRows)
	}
	rows, columns := int(rowCount), int(columnCount)
	if int64(rows)*int64(columns) > maxBytes {
		return malformed()
	}
	cells := make([]any, rows*columns)
	out := make([][]any, rows)
	for r := range out {
		out[r] = cells[r*columns : (r+1)*columns : (r+1)*columns]
	}
	nulls := make([]bool, rows)
	var offsets []int
	for c := 0; c < columns; c++ {
		kind := d.byte()
		clear(nulls)
		present := rows
		if kind&columnarHasNulls != 0 {
			kind &^= columnarHasNulls
			if kind == columnarKindNull || kind == columnarKindMixed {
				return malformed()
			}
			bitmap := d.take(uint64((rows + 7) / 8))
			if d.failed {
				return malformed()
			}
			for r := range nulls {
				if bitmap[r/8]&(1<<(r%8)) != 0 {
					nulls[r] = true
					present--
				}
			}
		}
		switch kind {
		case columnarKindNull:
		case columnarKindBool:
			for r := range rows {
				if !nulls[r] {
					cells[r*columns+c] = d.bool()
				}
			}
		case columnarKindInt:
			for r := range rows {
				if !nulls[r] {
					cells[r*columns+c] = d.int()
				}
			}
		case columnarKindFloat:
			for r := range rows {
				if !nulls[r] {
					cells[r*columns+c] = d.float()
				}
			}
		case columnarKindString, columnarKindJSON:
			var block string
			block, offsets = d.texts(present, offsets)
			if d.failed {
				return malformed()
			}
			next := 0
			for r := range rows {
				if nulls[r] {
					continue
				}
				text := block[offsets[next]:offsets[next+1]]
				next++
				if kind == columnarKindString {
					cells[r*columns+c] = text
					continue
				}
				value, ok := databaseDriverJSONCell(text)
				if !ok {
					return malformed()
				}
				cells[r*columns+c] = value
			}
		case columnarKindMixed:
			for r := range rows {
				switch tag := d.byte(); tag {
				case columnarTagNull:
				case columnarTagFalse:
					cells[r*columns+c] = false
				case columnarTagTrue:
					cells[r*columns+c] = true
				case columnarTagInt:
					cells[r*columns+c] = d.int()
				case columnarTagFloat:
					cells[r*columns+c] = d.float()
				case columnarTagString, columnarTagJSON:
					raw := d.take(d.uvarint())
					if d.failed || !utf8.Valid(raw) {
						return malformed()
					}
					if tag == columnarTagString {
						cells[r*columns+c] = string(raw)
						continue
					}
					value, ok := databaseDriverJSONCell(string(raw))
					if !ok {
						return malformed()
					}
					cells[r*columns+c] = value
				default:
					return malformed()
				}
			}
		default:
			return malformed()
		}
		if d.failed {
			return malformed()
		}
	}
	if d.i != len(d.b) {
		return malformed()
	}
	return out, nil
}
