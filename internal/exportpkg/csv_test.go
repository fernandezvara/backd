package exportpkg

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func jsonl(t *testing.T, docs ...map[string]any) *bytes.Reader {
	t.Helper()
	var b bytes.Buffer
	for _, d := range docs {
		line, err := EncodeDocument(d)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return bytes.NewReader(b.Bytes())
}

func TestWriteCSV(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	src := jsonl(t,
		map[string]any{"id": "b", "name": "Ana, \"the\" first", "n": int64(3), "ok": true, "tags": []any{"x", int64(1)}, "address": map[string]any{"city": "Vigo"}, "cipher": []byte{1, 2}, "_meta": map[string]any{"owner": "u1", "created_at": at}},
		map[string]any{"id": "a", "name": "=SUM(A1)", "extra": nil, "address": map[string]any{"zip": "36"}, "empty": map[string]any{}, "_meta": map[string]any{"owner": nil}},
	)
	var out bytes.Buffer
	rows, err := WriteCSV(src, &out, nil)
	if err != nil || rows != 2 {
		t.Fatalf("%d %v", rows, err)
	}
	want := "id,address.city,address.zip,cipher,empty,extra,n,name,ok,tags,_meta.created_at,_meta.owner\n" +
		`b,Vigo,,AQI=,,,3,"Ana, ""the"" first",true,"[""x"",1]",2026-10-09T08:00:00Z,u1` + "\n" +
		`a,,36,,{},,,'=SUM(A1),,,,` + "\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestWriteCSVFieldOrder(t *testing.T) {
	src := jsonl(t, map[string]any{"id": "a", "z": "1", "a": "2", "address": map[string]any{"zip": "3", "city": "4"}})
	var out bytes.Buffer
	if _, err := WriteCSV(src, &out, []string{"z", "address", "a"}); err != nil {
		t.Fatal(err)
	}
	if first := strings.SplitN(out.String(), "\n", 2)[0]; first != "id,z,address.city,address.zip,a" {
		t.Errorf("header: %s", first)
	}
}

func TestWriteCSVEmptyAndBroken(t *testing.T) {
	var out bytes.Buffer
	if rows, err := WriteCSV(strings.NewReader(""), &out, nil); err != nil || rows != 0 || out.String() != "\n" {
		t.Errorf("empty: %d %v %q", rows, err, out.String())
	}
	if _, err := WriteCSV(strings.NewReader("{nope\n"), &out, nil); err == nil {
		t.Error("a broken line was accepted")
	}
}
