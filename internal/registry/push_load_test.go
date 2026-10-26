package registry

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

// pushTree writes a realm "shop" with the given realm.yaml and files and returns its root.
func pushTree(t *testing.T, realmYAML string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	all := map[string]string{
		"shop/realm.yaml":                           realmYAML,
		"shop/notify/_functions/deno.json":          `{}`,
		"shop/notify/_functions/ship/index.ts":      "export default () => 1;\n",
		"shop/notify/_functions/ship/function.yaml": "invoke: \"true\"\n",
	}
	maps.Copy(all, files)
	for p, content := range all {
		writeFile(t, filepath.Join(root, p), content)
	}
	return root
}

const pushRealm = "locales: [en, es]\npush:\n  android: {provider: capture}\n"

func TestPushTemplatesAndFunctionsLoad(t *testing.T) {
	root := pushTree(t, pushRealm, map[string]string{
		"shop/_realm/push/order-shipped/en.yaml":    "title: Shipped\nbody: On its way\n",
		"shop/_realm/push/order-shipped/es.yaml":    "title: Enviado\nbody: De camino\n",
		"shop/notify/_functions/ship/function.yaml": "invoke: \"true\"\npush:\n  kinds: [order-shipped]\n  audience: true\n",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	rl := reg.Realms["shop"]
	if rl.Push == nil || !rl.Push.Has("order-shipped", "es") {
		t.Fatalf("templates: %+v", rl.Push)
	}
	fp := rl.Databases["notify"].Functions.Functions["ship"].Push
	if fp == nil || !fp.Restricted || !fp.Audience || !fp.AllowsKind("order-shipped") || fp.AllowsKind("other") {
		t.Errorf("function push: %+v", fp)
	}
	// The templates are part of the fingerprint.
	before, err := reg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "shop/_realm/push/order-shipped/en.yaml"), "title: Shipped!\nbody: On its way\n")
	reg2, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := reg2.Fingerprint(); after == before {
		t.Error("a push template doesn't change the fingerprint")
	}
}

func TestPushLoadErrors(t *testing.T) {
	good := map[string]string{
		"shop/_realm/push/order-shipped/en.yaml": "title: Shipped\nbody: On its way\n",
		"shop/_realm/push/order-shipped/es.yaml": "title: Enviado\nbody: De camino\n",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		maps.Copy(m, good)
		maps.Copy(m, extra)
		return m
	}
	for name, c := range map[string]struct {
		realm string
		files map[string]string
		want  string
	}{
		"a language lacks it":  {pushRealm, map[string]string{"shop/_realm/push/order-shipped/en.yaml": "title: a\nbody: b\n"}, "es.yaml: missing"},
		"declared, no folder":  {pushRealm, with(map[string]string{"shop/notify/_functions/ship/function.yaml": "invoke: \"true\"\npush: {kinds: [nope]}\n"}), `declares the push kind "nope"`},
		"declared, no push":    {"locales: [en]\n", map[string]string{"shop/notify/_functions/ship/function.yaml": "invoke: \"true\"\npush: true\n"}, "declares `push`, but this realm has no push section"},
		"bad declaration":      {pushRealm, with(map[string]string{"shop/notify/_functions/ship/function.yaml": "invoke: \"true\"\npush: {kind: [a]}\n"}), "push.kind: unknown key"},
		"push is not a string": {pushRealm, with(map[string]string{"shop/notify/_functions/ship/function.yaml": "invoke: \"true\"\npush: yes please\n"}), "push: must be true"},
	} {
		_, err := Load(pushTree(t, c.realm, c.files))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
	// A realm without push: ignores a push folder, and `push` is not a database.
	root := pushTree(t, "locales: [en]\n", map[string]string{"shop/_realm/push/x/en.yaml": "title: a\nbody: b\n"})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Realms["shop"].Databases["push"]; ok {
		t.Error("the push folder was read as a database")
	}
}
