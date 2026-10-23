package exportpkg

import (
	"bufio"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
)

// WriteCSV turns a JSON Lines stream of documents into one RFC 4180 table, and returns how
// many rows it wrote. The columns are the union of every document's flattened paths: nested
// objects become "a.b" columns, `_meta` becomes "_meta.owner" and so on, arrays become JSON
// text in one cell, dates RFC 3339 text, binary values (ciphertext) base64 text, null and
// missing empty. "id" comes first; then the columns of fields in the order given (all of them,
// sorted, when none is), `_meta` last. It reads the stream twice (columns, then rows), so the
// source must seek. A text cell that a spreadsheet would run as a formula (it starts with
// =, +, - or @) gets a leading apostrophe.
func WriteCSV(src io.ReadSeeker, w io.Writer, fields []string) (int64, error) {
	cols := map[string]bool{}
	if err := ReadDocuments(src, func(_ int, doc map[string]any) error {
		flatten("", doc, func(k string, _ string) { cols[k] = true })
		return nil
	}); err != nil {
		return 0, err
	}
	header := orderColumns(cols, fields)
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	bw := bufio.NewWriter(w)
	cw := csv.NewWriter(bw)
	if err := cw.Write(header); err != nil {
		return 0, err
	}
	var rows int64
	err := ReadDocuments(src, func(_ int, doc map[string]any) error {
		cells := map[string]string{}
		flatten("", doc, func(k, v string) { cells[k] = v })
		rec := make([]string, len(header))
		for i, c := range header {
			rec[i] = cells[c]
		}
		rows++
		return cw.Write(rec)
	})
	if err != nil {
		return 0, err
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, err
	}
	return rows, bw.Flush()
}

func orderColumns(cols map[string]bool, fields []string) []string {
	var header []string
	used := map[string]bool{}
	add := func(c string) {
		if cols[c] && !used[c] {
			used[c] = true
			header = append(header, c)
		}
	}
	add("id")
	if len(fields) > 0 {
		for _, f := range fields {
			add(f)
			var under []string
			for c := range cols {
				if strings.HasPrefix(c, f+".") {
					under = append(under, c)
				}
			}
			sort.Strings(under)
			for _, c := range under {
				add(c)
			}
		}
	}
	rest := make([]string, 0, len(cols))
	for c := range cols {
		if !used[c] {
			rest = append(rest, c)
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		mi, mj := strings.HasPrefix(rest[i], "_meta."), strings.HasPrefix(rest[j], "_meta.")
		if mi != mj {
			return mj
		}
		return rest[i] < rest[j]
	})
	return append(header, rest...)
}

// flatten calls emit with every leaf of v under prefix and its cell text.
func flatten(prefix string, v any, emit func(key, cell string)) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 && prefix != "" {
			emit(prefix, "{}")
			return
		}
		for k, e := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, e, emit)
		}
	case nil:
		emit(prefix, "")
	case string:
		emit(prefix, safeCell(t))
	case bool:
		emit(prefix, strconv.FormatBool(t))
	case int64:
		emit(prefix, strconv.FormatInt(t, 10))
	case float64:
		emit(prefix, strconv.FormatFloat(t, 'g', -1, 64))
	case time.Time:
		emit(prefix, t.UTC().Format(time.RFC3339Nano))
	case []byte:
		emit(prefix, base64.StdEncoding.EncodeToString(t))
	case fieldcrypt.Ciphertext:
		emit(prefix, base64.StdEncoding.EncodeToString(t)) // an encrypted value nobody opened
	case []any:
		b, _ := json.Marshal(toExtendedText(t))
		emit(prefix, safeCell(string(b)))
	default:
		emit(prefix, fmt.Sprint(t))
	}
}

// toExtendedText prepares an array for its JSON cell: dates and binary as the text they have
// in the other cells.
func toExtendedText(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case []byte:
		return base64.StdEncoding.EncodeToString(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = toExtendedText(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = toExtendedText(e)
		}
		return out
	}
	return v
}

func safeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
