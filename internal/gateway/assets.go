package gateway

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/BohuaiSong2004/study-abroad-assistant/web"
)

// assets serves the UI with fingerprinted URLs: at load time every HTML file's
// /css, /js, /vendor and /img references get ?v=<content hash>, and requests
// carrying the current hash are cacheable for a year (immutable). HTML itself
// is always revalidated (ETag), so a deploy is picked up on the next load
// without stale scripts. Text assets are gzip-compressed once at startup.
type assets struct {
	dir   string // non-empty: reload from disk on every request (development)
	mu    sync.RWMutex
	files map[string]*asset
}

type asset struct {
	body, gz []byte
	ctype    string
	etag     string
	hash     string
	// zh is the Chinese variant of an HTML page: same markup, served with
	// <html lang="zh-CN" class="i18n-pending"> so it stays hidden until the
	// page script has translated it (see lang.go and js/i18n-zh.js).
	zh *asset
}

var refRE = regexp.MustCompile(`(src|href)="(/(?:css|js|vendor|img)/[^"?#]+)"`)

func newAssets(dir string) (*assets, error) {
	a := &assets{dir: dir}
	return a, a.load()
}

func (a *assets) fsys() (fs.FS, error) {
	if a.dir != "" {
		return os.DirFS(a.dir), nil
	}
	return fs.Sub(web.Static, "static")
}

func (a *assets) load() error {
	fsys, err := a.fsys()
	if err != nil {
		return err
	}
	files := map[string]*asset{}
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = "application/octet-stream"
		}
		sum := sha256.Sum256(b)
		files["/"+p] = &asset{body: b, ctype: ct, hash: hex.EncodeToString(sum[:])[:12]}
		return nil
	})
	if err != nil {
		return err
	}
	for name, f := range files {
		if strings.HasSuffix(name, ".html") {
			f.zh = &asset{ctype: f.ctype}
			f.body = refRE.ReplaceAllFunc(f.body, func(m []byte) []byte {
				sub := refRE.FindSubmatch(m)
				if ref, ok := files[string(sub[2])]; ok {
					return []byte(string(sub[1]) + `="` + string(sub[2]) + "?v=" + ref.hash + `"`)
				}
				return m
			})
			sum := sha256.Sum256(f.body)
			f.hash = hex.EncodeToString(sum[:])[:12]
			f.zh.body = bytes.Replace(f.body, []byte(`<html lang="en">`), []byte(`<html lang="zh-CN" class="i18n-pending">`), 1)
			f.zh.hash = f.hash + "-zh"
			f.zh.finish()
		}
		f.finish()
	}
	a.mu.Lock()
	a.files = files
	a.mu.Unlock()
	return nil
}

// finish sets the ETag and the gzip body.
func (f *asset) finish() {
	f.etag = `"` + f.hash + `"`
	if compressible(f.ctype) && len(f.body) > 512 {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(f.body)
		_ = zw.Close()
		if buf.Len() < len(f.body) {
			f.gz = buf.Bytes()
		}
	}
}

func compressible(ct string) bool {
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") || strings.Contains(ct, "json") || strings.Contains(ct, "svg") || strings.Contains(ct, "xml")
}

func (a *assets) serve(w http.ResponseWriter, r *http.Request) {
	if a.dir != "" {
		_ = a.load()
	}
	p := path.Clean(r.URL.Path)
	if p == "/" {
		p = "/index.html"
	}
	a.mu.RLock()
	f, ok := a.files[p]
	a.mu.RUnlock()
	if !ok || strings.HasPrefix(path.Base(p), ".") {
		a.notFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Vary", "Accept-Encoding")
	if f.zh != nil {
		h.Set("Vary", "Accept-Encoding, Cookie, Accept-Language")
		if wantsChinese(r) {
			f = f.zh
		}
	}
	h.Set("Content-Type", f.ctype)
	h.Set("ETag", f.etag)
	switch {
	case strings.HasSuffix(p, ".html"):
		h.Set("Cache-Control", "no-cache")
	case r.URL.Query().Get("v") == f.hash:
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		h.Set("Cache-Control", "no-cache")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, f.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := f.body
	if f.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = f.gz
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func (a *assets) notFound(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	f, ok := a.files["/404.html"]
	a.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if f.zh != nil && wantsChinese(r) {
		f = f.zh
	}
	w.Header().Set("Content-Type", f.ctype)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(f.body)
}
