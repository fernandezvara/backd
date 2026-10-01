package executor

import "testing"

// Secrets and the values backd asks to mask are both hidden from a
// function's logs and error message.
func TestMaskValues(t *testing.T) {
	env := Envelope{Secrets: map[string]string{"KEY": "sk_live_123"}, Mask: []string{"tok-abc"}}
	got := mask("sent https://x/verify?token=tok-abc with sk_live_123", maskValues(env))
	if want := "sent https://x/verify?token=*** with ***"; got != want {
		t.Errorf("mask = %q, want %q", got, want)
	}
	logs := parseLogs(`{"level":"log","line":"link tok-abc"}`+"\n", maskValues(env))
	if len(logs) != 1 || logs[0].Line != "link ***" {
		t.Errorf("logs = %+v", logs)
	}
	if mask("unchanged", maskValues(Envelope{})) != "unchanged" {
		t.Error("nothing to mask changed the text")
	}
}
