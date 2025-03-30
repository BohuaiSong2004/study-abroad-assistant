package ids

import (
	"sort"
	"testing"
	"time"
)

func TestIDsAreUniqueSortableAndValidated(t *testing.T) {
	seen := map[string]bool{}
	var list []string
	for i := 0; i < 2000; i++ {
		id := New("app_")
		if seen[id] {
			t.Fatal("duplicate id")
		}
		if !Valid("app_", id) {
			t.Fatalf("invalid %s", id)
		}
		seen[id] = true
		list = append(list, id)
		if i%500 == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	first, last := list[0], list[len(list)-1]
	sorted := append([]string(nil), list...)
	sort.Strings(sorted)
	if sorted[0] != first && sorted[0][:12] > first[:12] {
		t.Fatal("ids do not sort by creation time")
	}
	if last[:10] < first[:10] {
		t.Fatal("later id sorts before earlier id")
	}
	for _, bad := range []string{"", "app_", "usr_" + New("")[0:26], "app_" + "ILOU" + New("")[4:26], "app_short"} {
		if Valid("app_", bad) {
			t.Errorf("Valid(%q)", bad)
		}
	}
	if a, b := Token(), Token(); len(a) < 50 || a == b {
		t.Fatal("token")
	}
}
