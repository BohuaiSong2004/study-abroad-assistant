package gateway

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/timeline"
	"github.com/BohuaiSong2004/study-abroad-assistant/web"
)

// The Chinese UI must not silently fall back to English: every string the
// pages can show has to be in js/i18n-zh.js. Keys are computed here the way
// WP.translate computes them in the browser.

func staticFS(t *testing.T) fs.FS {
	t.Helper()
	f, err := fs.Sub(web.Static, "static")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

var zhKeyRE = regexp.MustCompile(`(?m)^\s*("(?:[^"\\]|\\.)*")\s*:\s*("(?:[^"\\]|\\.)*")`)

func zhDict(t *testing.T) map[string]string {
	t.Helper()
	b, err := fs.ReadFile(staticFS(t), "js/i18n-zh.js")
	if err != nil {
		t.Fatal(err)
	}
	d := map[string]string{}
	for _, m := range zhKeyRE.FindAllSubmatch(b, -1) {
		var k, v string
		if json.Unmarshal(m[1], &k) != nil || json.Unmarshal(m[2], &v) != nil {
			t.Fatalf("bad entry %s", m[0])
		}
		if _, dup := d[k]; dup {
			t.Errorf("duplicate key %q", k)
		}
		d[k] = v
	}
	if len(d) < 100 {
		t.Fatalf("parsed only %d entries", len(d))
	}
	return d
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// htmlKeys returns the data-i18n keys (text with {0}, {1} for child
// elements) and data-i18n-attr values of every page.
func htmlKeys(t *testing.T) map[string]string {
	t.Helper()
	keys := map[string]string{}
	pages, _ := fs.Glob(staticFS(t), "*.html")
	for _, p := range pages {
		b, _ := fs.ReadFile(staticFS(t), p)
		doc, err := html.Parse(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode {
				for _, a := range n.Attr {
					switch a.Key {
					case "data-i18n":
						var sb strings.Builder
						i := 0
						for c := n.FirstChild; c != nil; c = c.NextSibling {
							switch c.Type {
							case html.TextNode:
								sb.WriteString(c.Data)
							case html.ElementNode:
								sb.WriteString("{" + string(rune('0'+i)) + "}")
								i++
							}
						}
						keys[collapse(sb.String())] = p + " <" + n.Data + ">"
					case "data-i18n-attr":
						for _, name := range strings.Split(a.Val, ",") {
							for _, b := range n.Attr {
								if b.Key == name {
									keys[b.Val] = p + " @" + name
								}
							}
						}
					}
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(doc)
	}
	return keys
}

var jsStringRE = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// jsKeys returns the string literals of every WP.t call's first argument
// (both branches of a ternary count).
func jsKeys(t *testing.T) map[string]string {
	t.Helper()
	keys := map[string]string{}
	files, _ := fs.Glob(staticFS(t), "js/*.js")
	for _, f := range files {
		if strings.HasSuffix(f, "i18n-zh.js") {
			continue
		}
		b, _ := fs.ReadFile(staticFS(t), f)
		s := string(b)
		for i := strings.Index(s, "WP.t("); i >= 0; {
			j, depth := i+5, 1
		scan:
			for ; j < len(s) && depth > 0; j++ {
				switch s[j] {
				case '"':
					for j++; s[j] != '"'; j++ {
						if s[j] == '\\' {
							j++
						}
					}
				case '(', '[', '{':
					depth++
				case ')', ']', '}':
					depth--
				case ',':
					if depth == 1 {
						break scan
					}
				}
			}
			for _, lit := range jsStringRE.FindAllString(s[i+5:j], -1) {
				var k string
				if err := json.Unmarshal([]byte(lit), &k); err != nil {
					t.Fatalf("%s: %s: %v", f, lit, err)
				}
				keys[k] = f
			}
			next := strings.Index(s[i+5:], "WP.t(")
			if next < 0 {
				break
			}
			i += 5 + next
		}
	}
	return keys
}

// plannerCodes runs the planner through every branch that emits text.
func plannerCodes(t *testing.T) map[string]string {
	t.Helper()
	codes := map[string]string{}
	add := func(tx *timelinev1.Text, where string) {
		if tx != nil {
			codes[tx.Code] = where
		}
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	docs := []catalogv1.DocumentType{}
	for d := range catalogv1.DocumentType_name {
		if d != 0 {
			docs = append(docs, catalogv1.DocumentType(d))
		}
	}
	prog := func(toefl, ielts int32, rounds ...*catalogv1.Deadline) *catalogv1.Program {
		return &catalogv1.Program{Id: "p", Timezone: "UTC", Deadlines: rounds, ApplicationFeeCents: 9000, Currency: "USD",
			Requirements: &catalogv1.Requirements{Documents: docs, MinToefl: toefl, MinIeltsTenths: ielts, RecommendationsRequired: 3}}
	}
	r1 := &catalogv1.Deadline{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Date: "2027-01-15"}
	r2 := &catalogv1.Deadline{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_2, Date: "2027-03-01"}
	recs := []*appsv1.RecommendationSummary{
		{Id: "a", RecommenderName: "Ada", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED},
		{Id: "b", RecommenderName: "Bo", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_DECLINED},
	}
	enough := []*appsv1.RecommendationSummary{{Id: "a", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED}}
	cases := []struct {
		p *catalogv1.Program
		a *appsv1.Application
	}{
		{prog(100, 70, r1, r2), &appsv1.Application{Id: "x", Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Recommendations: recs}},
		{prog(100, 0, r2), &appsv1.Application{Id: "x", Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Status: appsv1.ApplicationStatus_APPLICATION_STATUS_WITHDRAWN}},
		{prog(0, 65, r1), &appsv1.Application{Id: "x", Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1}},
		{prog(0, 0), &appsv1.Application{Id: "x", Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1}},
	}
	cases[2].p.Requirements.RecommendationsRequired = 1
	cases[2].a.Recommendations = enough
	for i, c := range cases {
		tl := timeline.Plan(c.p, c.a, nil, now)
		for _, m := range tl.Milestones {
			add(m.TitleText, "planner")
			add(m.DescriptionText, "planner")
			if m.Source == timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED && m.TitleText == nil {
				t.Errorf("case %d: generated milestone %s has no structured title", i, m.Id)
			}
		}
		if len(tl.WarningTexts) != len(tl.Warnings) {
			t.Errorf("case %d: %d warnings but %d structured", i, len(tl.Warnings), len(tl.WarningTexts))
		}
		for _, w := range tl.WarningTexts {
			add(w, "planner")
		}
	}
	return codes
}

var placeholderRE = regexp.MustCompile(`\{[a-z]+\}|\{\d\}`)

func placeholders(s string) string {
	p := placeholderRE.FindAllString(s, -1)
	sort.Strings(p)
	return strings.Join(p, " ")
}

func TestChineseDictionaryCoversTheUI(t *testing.T) {
	dict := zhDict(t)
	need := map[string]string{}
	for k, v := range htmlKeys(t) {
		need[k] = v
	}
	for k, v := range jsKeys(t) {
		need[k] = v
	}
	codes := plannerCodes(t)
	if len(codes) < 20 {
		t.Fatalf("planner emitted only %d codes", len(codes))
	}
	for c := range codes {
		need["code:"+c] = "planner"
	}
	var missing []string
	for k, where := range need {
		if k == "Waypoint" {
			continue
		}
		v, ok := dict[k]
		if !ok {
			missing = append(missing, where+": "+k)
			continue
		}
		if !strings.HasPrefix(k, "code:") && placeholders(k) != placeholders(v) {
			t.Errorf("placeholders differ for %q: %q", k, v)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Error("missing Chinese text: " + m)
	}
	if len(need) < 200 {
		t.Fatalf("found only %d UI strings; the extraction is broken", len(need))
	}
}
