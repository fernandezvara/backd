package client

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The vectors are shared with the server, backd-js and the Dart client, so all keep one grammar.
type sizeVectors struct {
	Parse []struct {
		In         string `json:"in"`
		Family     string `json:"family"`
		AllowBytes bool   `json:"allowBytes"`
		Bytes      int64  `json:"bytes"`
		Error      bool   `json:"error"`
	} `json:"parse"`
	Format []struct {
		Bytes  int64  `json:"bytes"`
		Family string `json:"family"`
		Out    string `json:"out"`
	} `json:"format"`
}

func familyOf(s string) SizeFamily {
	switch s {
	case "decimal":
		return SizeDecimal
	case "binary":
		return SizeBinary
	}
	return SizeBoth
}

func loadSizeVectors(t *testing.T) sizeVectors {
	t.Helper()
	raw, err := os.ReadFile("../../clients/testdata/size-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v sizeVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseSizeVectors(t *testing.T) {
	for _, c := range loadSizeVectors(t).Parse {
		got, err := ParseSize(c.In, familyOf(c.Family), c.AllowBytes)
		if c.Error {
			if err == nil {
				t.Errorf("ParseSize(%q, %s, %v) accepted: %d", c.In, c.Family, c.AllowBytes, got)
			}
			continue
		}
		if err != nil || got != c.Bytes {
			t.Errorf("ParseSize(%q, %s, %v) = %d, %v; want %d", c.In, c.Family, c.AllowBytes, got, err, c.Bytes)
		}
	}
}

func TestFormatSizeVectors(t *testing.T) {
	for _, c := range loadSizeVectors(t).Format {
		if got := FormatSize(c.Bytes, familyOf(c.Family)); got != c.Out {
			t.Errorf("FormatSize(%d, %s) = %q; want %q", c.Bytes, c.Family, got, c.Out)
		}
	}
}

func TestParseSizeMessages(t *testing.T) {
	_, err := ParseSize("2MB", SizeBinary, false)
	if err == nil || !strings.Contains(err.Error(), "KiB, MiB, GiB, TiB") {
		t.Errorf("a decimal unit in a binary field: %v", err)
	}
	_, err = ParseSize("0.1KiB", SizeBoth, false)
	if err == nil || !strings.Contains(err.Error(), "whole number of bytes") {
		t.Errorf("a fraction of a byte: %v", err)
	}
}
