package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWantsChinese(t *testing.T) {
	for _, c := range []struct {
		cookie, accept string
		want           bool
	}{
		{"", "", false},
		{"", "zh-CN,zh;q=0.9,en;q=0.8", true},
		{"", "en-US,en;q=0.9,zh-CN;q=0.8", false},
		{"", "fr-FR,zh-TW;q=0.9,en;q=0.5", true}, // first of zh/en by preference
		{"", "en;q=0.2,zh;q=0.9", true},          // q wins over order
		{"", "zh;q=0,en", false},                 // q=0 means "not acceptable"
		{"zh", "en-US", true},                    // the user's choice wins
		{"en", "zh-CN", false},
		{"klingon", "zh-CN", true}, // junk cookie: fall back to the header
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: langCookie, Value: c.cookie})
		}
		if got := wantsChinese(r); got != c.want {
			t.Errorf("cookie=%q accept=%q: got %v", c.cookie, c.accept, got)
		}
	}
}

func TestPagesAreServedInTheNegotiatedLanguage(t *testing.T) {
	a, err := newAssets("")
	if err != nil {
		t.Fatal(err)
	}
	get := func(lang string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/dashboard.html", nil)
		r.Header.Set("Accept-Language", lang)
		w := httptest.NewRecorder()
		a.serve(w, r)
		return w
	}
	en, zh := get("en-US"), get("zh-CN")
	if !strings.Contains(en.Body.String(), `<html lang="en">`) || !strings.Contains(zh.Body.String(), `<html lang="zh-CN" class="i18n-pending">`) {
		t.Fatal("variants not served")
	}
	if en.Header().Get("ETag") == zh.Header().Get("ETag") {
		t.Fatal("variants share an ETag; a cache could serve one for the other")
	}
	if v := zh.Header().Get("Vary"); !strings.Contains(v, "Cookie") || !strings.Contains(v, "Accept-Language") {
		t.Fatalf("Vary %q", v)
	}
	// Assets are not negotiated.
	r := httptest.NewRequest("GET", "/css/app.css", nil)
	r.Header.Set("Accept-Language", "zh-CN")
	w := httptest.NewRecorder()
	a.serve(w, r)
	if strings.Contains(w.Header().Get("Vary"), "Cookie") {
		t.Fatal("static assets must stay cacheable regardless of language")
	}
}
