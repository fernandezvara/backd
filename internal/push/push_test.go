package push

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTemplatesLoadAndRender(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "order-shipped/en.yaml", "title: \"Order {{.Data.order_no}} is on its way\"\nbody: \"It should arrive {{.Data.eta}}. ({{.Realm}}, {{.User.Locale}})\"\nlink: \"acme://orders/{{.Data.order_id}}\"\n")
	write(t, dir, "order-shipped/es.yaml", "title: \"Pedido {{.Data.order_no}} en camino\"\nbody: \"Llega {{.Data.eta}}.\"\n")
	tpl, errs := Load(dir, []string{"en", "es"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !tpl.Has("order-shipped", "es") || tpl.Has("order-shipped", "fr") || !tpl.HasKind("order-shipped") || tpl.HasKind("nope") {
		t.Errorf("has: %v", tpl.Kinds())
	}
	r, err := tpl.Render("order-shipped", "en", Data{Realm: "acme", Data: map[string]any{"order_no": "A-1", "eta": "tomorrow", "order_id": "42"}})
	if err != nil || r.Title != "Order A-1 is on its way" || r.Body != "It should arrive tomorrow. (acme, en)" || r.Link != "acme://orders/42" {
		t.Fatalf("render: %+v %v", r, err)
	}
	// A value the call didn't give renders as nothing, not "<no value>".
	r, _ = tpl.Render("order-shipped", "es", Data{Realm: "acme", Data: map[string]any{}})
	if r.Title != "Pedido  en camino" {
		t.Errorf("missing value: %q", r.Title)
	}
	if _, err := tpl.Render("order-shipped", "fr", Data{}); err == nil {
		t.Error("an unlisted locale rendered")
	}
}

func TestTemplatesErrors(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"missing locale": {map[string]string{"a/en.yaml": "title: t\nbody: b\n"}, "es.yaml: missing"},
		"no body":        {map[string]string{"a/en.yaml": "title: t\n", "a/es.yaml": "title: t\nbody: b\n"}, "title and body are required"},
		"bad template":   {map[string]string{"a/en.yaml": "title: \"{{.Data\"\nbody: b\n", "a/es.yaml": "title: t\nbody: b\n"}, "title"},
		"unknown key":    {map[string]string{"a/en.yaml": "title: t\nbody: b\nsound: x\n", "a/es.yaml": "title: t\nbody: b\n"}, "field sound not found"},
		"bad kind":       {map[string]string{"Bad_Kind/en.yaml": "title: t\nbody: b\n"}, "isn't a valid kind name"},
		"raw":            {map[string]string{"raw/en.yaml": "title: t\nbody: b\n"}, "not a template"},
	} {
		dir := t.TempDir()
		for p, content := range c.files {
			write(t, dir, p, content)
		}
		_, errs := Load(dir, []string{"en", "es"})
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Error(), c.want)
		}
		if !found {
			t.Errorf("%s: want %q, got %v", name, c.want, errs)
		}
	}
	// No folder, no templates.
	if tpl, errs := Load(filepath.Join(t.TempDir(), "nope"), []string{"en"}); len(errs) > 0 || len(tpl.Kinds()) != 0 {
		t.Errorf("missing folder: %v %v", errs, tpl.Kinds())
	}
}

func TestMessageValidation(t *testing.T) {
	ok := Message{Title: "Hi", Body: "There", Link: "acme://orders/1", Data: map[string]string{"a": "b"}, Group: "order-1", Priority: PriorityHigh}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	neg, big, huge := -1, 29*24*time.Hour, strings.Repeat("x", 3000)
	for name, mod := range map[string]func(*Message){
		"no title":       func(m *Message) { m.Title = "" },
		"no body":        func(m *Message) { m.Body = " " },
		"bad link":       func(m *Message) { m.Link = "not a url" },
		"group chars":    func(m *Message) { m.Group = "a b" },
		"group long":     func(m *Message) { m.Group = strings.Repeat("a", 33) },
		"badge":          func(m *Message) { m.Badge = &neg },
		"ttl":            func(m *Message) { m.TTL = &big },
		"priority":       func(m *Message) { m.Priority = "urgent" },
		"reserved key":   func(m *Message) { m.Data = map[string]string{"google.x": "1"} },
		"from key":       func(m *Message) { m.Data = map[string]string{"from": "1"} },
		"data too big":   func(m *Message) { m.Data = map[string]string{"k": huge} },
		"platform":       func(m *Message) { m.Platform = map[string]map[string]any{"windows": {}} },
		"control chars":  func(m *Message) { m.Title = "a\x00b" },
		"silent+title":   func(m *Message) { m.Silent = true },
		"silent no data": func(m *Message) { m.Silent, m.Title, m.Body, m.Link, m.Data = true, "", "", "", nil },
		"silent kind":    func(m *Message) { m.Silent, m.Title, m.Body, m.Kind = true, "", "", "order-shipped" },
		"silent badge":   func(m *Message) { b := 1; m.Silent, m.Title, m.Body, m.Badge = true, "", "", &b },
		"silent sound":   func(m *Message) { m.Silent, m.Title, m.Body, m.Sound = true, "", "", "default" },
	} {
		m := ok
		mod(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	silent := Message{Silent: true, Data: map[string]string{"sync": "1"}}
	if err := silent.Validate(); err != nil {
		t.Errorf("silent: %v", err)
	}
}

func TestShapesAndSizes(t *testing.T) {
	badge := 3
	ttl := time.Hour
	m := Message{Title: "Hi", Body: "There", Link: "acme://x", Data: map[string]string{"k": "v"}, Badge: &badge, Sound: "default", Group: "g", TTL: &ttl, Priority: PriorityHigh,
		Platform: map[string]map[string]any{"android": {"notification": map[string]any{"color": "#fff"}}, "ios": {"aps": map[string]any{"mutable-content": 1}}}}
	get := func(target Target) map[string]any {
		t.Helper()
		b, err := Shape(target, m)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	apns := get(Target{"ios", ProviderAPNs})
	aps := apns["aps"].(map[string]any)
	if aps["badge"] != float64(3) || aps["sound"] != "default" || aps["mutable-content"] != float64(1) || aps["thread-id"] != "g" || apns["link"] != "acme://x" || apns["k"] != "v" {
		t.Errorf("apns: %v", apns)
	}
	fcm := get(Target{"android", ProviderFCM})["message"].(map[string]any)
	android := fcm["android"].(map[string]any)
	if android["priority"] != "HIGH" || android["ttl"] != "3600s" || android["collapse_key"] != "g" || fcm["data"].(map[string]any)["link"] != "acme://x" ||
		android["notification"].(map[string]any)["color"] != "#fff" || android["notification"].(map[string]any)["sound"] != "default" {
		t.Errorf("fcm: %v", fcm)
	}
	web := get(Target{"web", ProviderWebPush})
	if web["url"] != "acme://x" || web["tag"] != "g" || web["title"] != "Hi" {
		t.Errorf("web: %v", web)
	}
	// A silent push is data only.
	s := Message{Silent: true, Data: map[string]string{"sync": "1"}}
	b, _ := Shape(Target{"ios", ProviderAPNs}, s)
	if !strings.Contains(string(b), `"content-available":1`) || strings.Contains(string(b), "alert") {
		t.Errorf("silent apns: %s", b)
	}
	// Sizes: the same message passes FCM and fails Web Push when the text sits near the limit.
	near := Message{Title: "t", Body: strings.Repeat("x", 3980)}
	for _, c := range []struct {
		t      Target
		tooBig bool
	}{{Target{"android", ProviderFCM}, false}, {Target{"ios", ProviderAPNs}, false}, {Target{"web", ProviderWebPush}, true}} {
		err := CheckSize([]Target{c.t}, near)
		var tl *TooLargeError
		if c.tooBig != (err != nil) || (err != nil && (!asTooLarge(err, &tl) || tl.Limit != c.t.Limit())) {
			t.Errorf("%s: %v", c.t.Platform, err)
		}
	}
	// The capture provider is as strict as the provider it stands for.
	if err := CheckSize([]Target{{"web", ProviderCapture}}, near); err == nil {
		t.Error("capture lets through what web push would refuse")
	}
	if err := CheckSize([]Target{{"android", ProviderFCM}}, Message{Title: "t", Body: strings.Repeat("x", 4000)}); err == nil {
		t.Error("4000 characters of body accepted by FCM")
	}
}

func asTooLarge(err error, dst **TooLargeError) bool {
	e, ok := err.(*TooLargeError)
	if ok {
		*dst = e
	}
	return ok
}

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "shipped/en.yaml", "title: \"Order {{.Data.no}} shipped\"\nbody: \"On its way\"\nlink: \"acme://orders/{{.Data.no}}\"\n")
	write(t, dir, "shipped/es.yaml", "title: \"Pedido {{.Data.no}} enviado\"\nbody: \"De camino\"\n")
	write(t, dir, "long/en.yaml", "title: t\nbody: \"{{.Data.text}}\"\n")
	write(t, dir, "long/es.yaml", "title: t\nbody: \"{{.Data.text}}\"\n")
	tpl, errs := Load(dir, []string{"en", "es"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	locales := []string{"en", "es"}
	targets := TargetsOf(map[string]string{"web": ProviderWebPush, "android": ProviderFCM})
	if len(targets) != 2 || targets[0].Platform != "android" {
		t.Fatalf("targets: %v", targets)
	}
	got, err := Prepare(tpl, "acme", locales, targets, "shipped", Message{Data: map[string]string{"no": "A-1"}, Group: "order-1"})
	if err != nil || got["en"].Title != "Order A-1 shipped" || got["es"].Title != "Pedido A-1 enviado" || got["en"].Link != "acme://orders/A-1" || got["es"].Link != "" || got["en"].Kind != "shipped" {
		t.Fatalf("templated: %+v %v", got, err)
	}
	// A raw message is the same text in every language.
	got, err = Prepare(tpl, "acme", locales, targets, "", Message{Title: "Hi", Body: "There"})
	if err != nil || got["en"].Title != "Hi" || got["es"].Title != "Hi" || got["es"].Kind != RawKind {
		t.Fatalf("raw: %+v %v", got, err)
	}
	var vErr *ValidationError
	for name, c := range map[string]struct {
		kind string
		m    Message
	}{
		"unknown kind":     {"nope", Message{}},
		"kind and text":    {"shipped", Message{Title: "x"}},
		"raw no body":      {"", Message{Title: "x"}},
		"bad priority":     {"shipped", Message{Priority: "x"}},
		"silent with kind": {"shipped", Message{Silent: true, Data: map[string]string{"a": "b"}}},
	} {
		if _, err := Prepare(tpl, "acme", locales, targets, c.kind, c.m); err == nil {
			t.Errorf("%s: accepted", name)
		} else if name == "unknown kind" && !errors.Is(err, ErrUnknownKind) {
			t.Errorf("%s: %v", name, err)
		} else if name != "unknown kind" && !errors.As(err, &vErr) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Too big for web push in one language names it.
	_, err = Prepare(tpl, "acme", locales, targets, "long", Message{Data: map[string]string{"text": strings.Repeat("x", 1900)}})
	if err != nil {
		t.Errorf("1900 should fit: %v", err)
	}
	_, err = Prepare(tpl, "acme", locales, targets, "", Message{Title: "t", Body: strings.Repeat("x", 3990)})
	var tl *TooLargeError
	if !errors.As(err, &tl) || tl.Platform != "web" {
		t.Errorf("too large: %v", err)
	}
}
