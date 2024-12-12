// Package civil handles calendar dates (YYYY-MM-DD) without a time of day.
// Application deadlines are civil dates in the program's time zone, so all
// date math happens on dates, never on instants, to avoid UTC drift.
package civil

import (
	"fmt"
	"time"
)

const layout = "2006-01-02"

// Date is a calendar date.
type Date struct{ t time.Time }

// Parse parses YYYY-MM-DD strictly.
func Parse(s string) (Date, error) {
	t, err := time.ParseInLocation(layout, s, time.UTC)
	if err != nil {
		return Date{}, fmt.Errorf("invalid date %q: want YYYY-MM-DD", s)
	}
	return Date{t}, nil
}

// MustParse is Parse that panics; for tests and constants.
func MustParse(s string) Date {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Today returns the current date in the named IANA zone (UTC if invalid).
func Today(now time.Time, zone string) Date {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// ValidZone reports whether zone is a loadable IANA time zone name.
func ValidZone(zone string) bool {
	if zone == "" || zone == "Local" {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}

func (d Date) String() string       { return d.t.Format(layout) }
func (d Date) IsZero() bool         { return d.t.IsZero() }
func (d Date) AddDays(n int) Date   { return Date{d.t.AddDate(0, 0, n)} }
func (d Date) Before(o Date) bool   { return d.t.Before(o.t) }
func (d Date) After(o Date) bool    { return d.t.After(o.t) }
func (d Date) Equal(o Date) bool    { return d.t.Equal(o.t) }
func (d Date) DaysUntil(o Date) int { return int(o.t.Sub(d.t).Hours() / 24) }
func (d Date) Compare(o Date) int   { return d.t.Compare(o.t) }
