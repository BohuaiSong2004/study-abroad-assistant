package gateway

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// langCookie holds the language the user picked in the header switch.
const langCookie = "wp_lang"

// wantsChinese decides which variant of a page to serve: the user's explicit
// choice (cookie) wins, otherwise the browser's Accept-Language preference
// between Chinese and English. Deciding on the server lets the page arrive
// with the right <html lang> and hidden until translated, without a
// render-blocking script for everyone.
func wantsChinese(r *http.Request) bool {
	if c, err := r.Cookie(langCookie); err == nil {
		switch c.Value {
		case "zh":
			return true
		case "en":
			return false
		}
	}
	type pref struct {
		tag string
		q   float64
		i   int
	}
	var prefs []pref
	for i, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		if tag == "" {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(f), "q="); ok {
				if n, err := strconv.ParseFloat(v, 64); err == nil {
					q = n
				}
			}
		}
		prefs = append(prefs, pref{tag, q, i})
	}
	sort.SliceStable(prefs, func(a, b int) bool { return prefs[a].q > prefs[b].q })
	for _, p := range prefs {
		switch {
		case p.q <= 0:
		case p.tag == "zh" || strings.HasPrefix(p.tag, "zh-"):
			return true
		case p.tag == "en" || strings.HasPrefix(p.tag, "en-"):
			return false
		}
	}
	return false
}
