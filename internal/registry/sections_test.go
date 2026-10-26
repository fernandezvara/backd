package registry

import (
	"strings"
	"testing"
)

func TestDecodeSection(t *testing.T) {
	type doc struct {
		Realtime bool `yaml:"realtime"`
	}
	for name, tt := range map[string]struct {
		yaml    string
		own     []string
		wantOK  bool
		wantErr string
		wantRT  bool
	}{
		"empty file":                          {"", nil, true, "", false},
		"valid":                               {"realtime: true\n", nil, true, "", true},
		"own mistake is reported":             {"realtime: [1]\n", []string{"realtime"}, false, "c.yaml:", false},
		"other mistake is not ours":           {"realtime: [1]\n", []string{"triggers"}, false, "", false},
		"no words: always left to the others": {"realtime: [1]\n", nil, false, "", false},
	} {
		var d doc
		ok, err := decodeSection([]byte(tt.yaml), "c.yaml", &d, tt.own...)
		if ok != tt.wantOK || d.Realtime != tt.wantRT {
			t.Errorf("%s: ok=%v realtime=%v", name, ok, d.Realtime)
		}
		if (tt.wantErr == "") != (err == nil) || (err != nil && !strings.HasPrefix(err.Error(), tt.wantErr)) {
			t.Errorf("%s: err = %v, want prefix %q", name, err, tt.wantErr)
		}
	}
}
