package db

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// sqlStatementReader splits a SQL script into statements as it streams, so a
// multi-gigabyte dump never has to fit in memory. Delimiters inside quoted
// strings, quoted identifiers, PostgreSQL dollar-quoted bodies and comments
// do not end a statement. A "DELIMITER x" line (mysqldump routines) is a
// client directive: it switches the delimiter and is never executed.
type sqlStatementReader struct {
	r         *bufio.Reader
	delimiter string
	// maxBytes bounds one statement, so a runaway quote cannot consume the
	// whole file into memory.
	maxBytes int
}

const sqlRestoreMaxStatement = 256 << 20

var (
	errUnterminatedSQL   = errors.New("SQL 脚本以未闭合的字符串或注释结束，文件可能不完整")
	errStatementTooLarge = errors.New("单条 SQL 语句超过 256 MiB，已停止恢复")
)

func newSQLStatementReader(r io.Reader) *sqlStatementReader {
	return &sqlStatementReader{r: bufio.NewReaderSize(r, 1<<20), delimiter: ";", maxBytes: sqlRestoreMaxStatement}
}

// next returns the next statement without its delimiter, or io.EOF.
// Fragments that hold only comments are skipped.
func (s *sqlStatementReader) next() (string, error) {
	var b []byte
	var quote byte // ' " `
	dollar := ""   // active $tag$
	lineComment, blockComment := false, false
	for {
		c, err := s.r.ReadByte()
		if err == io.EOF {
			if quote != 0 || dollar != "" || blockComment {
				return "", errUnterminatedSQL
			}
			stmt := strings.TrimSpace(string(b))
			if stmt == "" || sqlOnlyComments(stmt) {
				return "", io.EOF
			}
			return stmt, nil
		}
		if err != nil {
			return "", err
		}
		if len(b) >= s.maxBytes {
			return "", errStatementTooLarge
		}
		if quote == 0 && dollar == "" && !lineComment && !blockComment && (c == 'D' || c == 'd') &&
			strings.TrimSpace(string(b)) == "" && s.delimiterDirective() {
			b = b[:0]
			continue
		}
		b = append(b, c)
		switch {
		case lineComment:
			if c == '\n' {
				lineComment = false
			}
			continue
		case blockComment:
			if c == '/' && len(b) >= 2 && b[len(b)-2] == '*' {
				blockComment = false
			}
			continue
		case quote != 0:
			if c == '\\' && quote != '`' {
				if n, err := s.r.ReadByte(); err == nil {
					b = append(b, n)
				}
			} else if c == quote {
				// A doubled quote is an escaped quote, not the end.
				if n, _ := s.r.Peek(1); len(n) == 1 && n[0] == quote {
					_, _ = s.r.ReadByte()
					b = append(b, quote)
				} else {
					quote = 0
				}
			}
			continue
		case dollar != "":
			if c == '$' && len(b) >= len(dollar) && string(b[len(b)-len(dollar):]) == dollar {
				dollar = ""
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '-':
			if n, _ := s.r.Peek(1); len(n) == 1 && n[0] == '-' {
				lineComment = true
			}
		case '#':
			lineComment = true
		case '/':
			if n, _ := s.r.Peek(1); len(n) == 1 && n[0] == '*' {
				_, _ = s.r.ReadByte()
				b = append(b, '*')
				blockComment = true
			}
		case '$':
			if tag := s.dollarTag(); tag != "" {
				b = append(b, tag[1:]...)
				dollar = tag
			}
		}
		if d := s.delimiter; len(b) >= len(d) && string(b[len(b)-len(d):]) == d {
			stmt := strings.TrimSpace(string(b[:len(b)-len(d)]))
			b = b[:0]
			if stmt != "" && !sqlOnlyComments(stmt) {
				return stmt, nil
			}
		}
	}
}

// delimiterDirective consumes a "DELIMITER x" line after its first letter
// and reports whether it was one.
func (s *sqlStatementReader) delimiterDirective() bool {
	peek, _ := s.r.Peek(9)
	if len(peek) < 9 || !strings.EqualFold(string(peek[:8]), "ELIMITER") || (peek[8] != ' ' && peek[8] != '\t') {
		return false
	}
	line, err := s.r.ReadString('\n')
	if err != nil && err != io.EOF {
		return false
	}
	if d := strings.TrimSpace(line[9:]); d != "" {
		s.delimiter = d
	}
	return true
}

// dollarTag reads the rest of a $tag$ opener after its first '$' and returns
// the whole "$tag$", or "" when this '$' does not open a dollar quote.
func (s *sqlStatementReader) dollarTag() string {
	for n := 1; n <= 64; n++ {
		peek, err := s.r.Peek(n)
		if err != nil {
			return ""
		}
		c := peek[n-1]
		if c == '$' {
			tag := "$" + string(peek)
			_, _ = s.r.Discard(n)
			return tag
		}
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || n > 1 && c >= '0' && c <= '9') {
			return ""
		}
	}
	return ""
}

// sqlOnlyComments reports a fragment holding nothing but comments. MySQL
// executable comments (/*! ... */) carry real statements and are kept.
func sqlOnlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "/*") && !strings.HasPrefix(t, "/*!") && strings.HasSuffix(t, "*/") {
			continue
		}
		return false
	}
	return true
}
