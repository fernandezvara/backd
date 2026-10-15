package registry

import (
	"strings"
	"testing"
)

func TestRealtimeOptIn(t *testing.T) {
	reg, err := salesTree(t, "realtime: true\n", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "sales")
	if !c.Realtime || !c.NeedsEvents() {
		t.Fatalf("Realtime = %v", c.Realtime)
	}
	got := reg.EventCollections()
	if len(got) != 1 || got[0].Realm != "shop" || got[0].MongoDatabase != "shop__main" || got[0].Name != "sales" || !got[0].Realtime {
		t.Fatalf("EventCollections = %+v", got)
	}
}

func TestRealtimeDefaultsOff(t *testing.T) {
	for _, yaml := range []string{"", "realtime: false\n", "cache: {public: false}\n"} {
		reg, err := salesTree(t, yaml, "")
		if err != nil {
			t.Fatalf("%q: %v", yaml, err)
		}
		c, _ := reg.Collection("shop", "main", "sales")
		if c.Realtime || c.NeedsEvents() {
			t.Errorf("%q: Realtime = true", yaml)
		}
		if n := len(reg.EventCollections()); n != 0 {
			t.Errorf("%q: %d event collections", yaml, n)
		}
	}
}

func TestRealtimeRefusesNonBoolean(t *testing.T) {
	_, err := salesTree(t, "realtime: \"yes please\"\n", "")
	if err == nil || !strings.Contains(err.Error(), "realtime must be true or false") || !strings.Contains(err.Error(), "collection.yaml") {
		t.Fatalf("err = %v", err)
	}
}
