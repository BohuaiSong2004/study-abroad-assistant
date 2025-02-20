package civil

import (
	"testing"
	"time"
)

func TestParseIsStrict(t *testing.T) {
	for _, bad := range []string{"", "2027-1-5", "2027-02-30", "12/01/2026", "2027-01-05T00:00:00Z"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	d := MustParse("2028-02-28")
	if d.AddDays(1).String() != "2028-02-29" || d.AddDays(2).String() != "2028-03-01" {
		t.Fatal("leap year arithmetic")
	}
	if !d.Before(d.AddDays(1)) || !d.AddDays(1).After(d) || !d.Equal(MustParse("2028-02-28")) || d.Compare(d.AddDays(-1)) != 1 {
		t.Fatal("comparisons")
	}
	if d.DaysUntil(MustParse("2029-02-28")) != 366 {
		t.Fatal("days until")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustParse did not panic")
		}
	}()
	MustParse("nope")
}

func TestTodayIsPerZone(t *testing.T) {
	now := time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC)
	if Today(now, "Asia/Tokyo").String() != "2027-01-01" {
		t.Fatal("Tokyo should already be in 2027")
	}
	if Today(now, "America/Los_Angeles").String() != "2026-12-31" {
		t.Fatal("Los Angeles is still on Dec 31")
	}
	if Today(now, "Bogus/Zone").String() != "2026-12-31" {
		t.Fatal("invalid zone falls back to UTC")
	}
	if ValidZone("") || ValidZone("Local") || ValidZone("Mars/Base") || !ValidZone("Europe/Berlin") {
		t.Fatal("ValidZone")
	}
}
