package gateway_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

func counselor(t *testing.T, e *env) *client {
	t.Helper()
	if err := e.stack.AccountService.EnsureAccount(context.Background(), "editor@example.com", "counselor-password", "Editor", 2); err != nil {
		t.Fatal(err)
	}
	c := e.client()
	c.expect(200, "POST", "/api/auth/login", map[string]any{"email": "editor@example.com", "password": "counselor-password"})
	return c
}

func TestCounselorEditsCatalogAndStudentsTimelineFollows(t *testing.T) {
	e := newEnv(t)
	editor := counselor(t, e)
	prog := map[string]any{
		"id": "harbor-ms-ds", "university": "Harbor Institute", "name": "MS Data Science", "degree": "master",
		"field": "Data Science", "country": "Canada", "city": "Halifax", "timezone": "America/Halifax", "intake": "Fall 2027",
		"deadlines":      []any{map[string]any{"round": "round_1", "date": "2027-01-10"}, map[string]any{"round": "final", "date": "2027-04-01"}},
		"requirements":   map[string]any{"minToefl": 95, "minIelts": 7, "greRequired": true, "recommendationsRequired": 2, "documents": []any{"statement_of_purpose", "cv"}},
		"applicationFee": map[string]any{"amountCents": 12500, "currency": "cad"}, "tags": []any{"STEM"},
	}
	created := editor.expect(201, "POST", "/api/programs", prog)
	if created["version"] != float64(1) || created["applicationFee"].(map[string]any)["currency"] != "CAD" {
		t.Fatalf("created %v", created)
	}
	docs := created["requirements"].(map[string]any)["documents"].([]any)
	if len(docs) != 4 { // + english_scores and gre_scores implied by the requirements
		t.Fatalf("documents %v", docs)
	}
	editor.expect(409, "POST", "/api/programs", prog)
	bad := map[string]any{}
	for k, v := range prog {
		bad[k] = v
	}
	bad["id"], bad["timezone"] = "harbor-bad", "Nowhere/City"
	editor.expect(400, "POST", "/api/programs", bad)

	// Public catalog endpoints.
	anon := e.client()
	list := anon.expect(200, "GET", "/api/programs?q=harbor&degree=master&country=Canada", nil)
	if ps := list["programs"].([]any); len(ps) != 1 {
		t.Fatalf("search %v", list)
	}
	anon.expect(400, "GET", "/api/programs?degree=wizard", nil)
	facets := anon.expect(200, "GET", "/api/programs/facets", nil)
	if !strings.Contains(strings.Join(toStrings(facets["countries"]), ","), "Canada") {
		t.Fatalf("facets %v", facets)
	}
	anon.expect(200, "GET", "/api/programs/harbor-ms-ds", nil)
	anon.expect(404, "GET", "/api/programs/missing", nil)

	// A student plans an application.
	student := e.client()
	student.register("planner@example.com")
	app := student.expect(201, "POST", "/api/applications", map[string]any{"programId": "harbor-ms-ds", "round": "round_1"})
	id := app["id"].(string)
	testutil.Eventually(t, 5*time.Second, func() bool {
		res, _ := student.do("GET", "/api/applications/"+id+"/timeline", nil)
		return res.StatusCode == 200
	}, "timeline not created")

	// The counselor moves the deadline; the student's timeline follows.
	created["deadlines"] = []any{map[string]any{"round": "round_1", "date": "2027-01-20"}, map[string]any{"round": "final", "date": "2027-04-01"}}
	updated := editor.expect(200, "PUT", "/api/programs/harbor-ms-ds", created)
	if updated["version"] != float64(2) {
		t.Fatalf("updated %v", updated)
	}
	editor.expect(409, "PUT", "/api/programs/harbor-ms-ds", created) // stale version
	testutil.Eventually(t, 5*time.Second, func() bool {
		tl := student.expect(200, "GET", "/api/applications/"+id+"/timeline", nil)
		return tl["deadline"] == "2027-01-20"
	}, "deadline change did not reach the student's timeline")

	sync := editor.expect(200, "GET", "/api/sync-status", nil)
	if streams := sync["streams"].([]any); len(streams) != 2 {
		t.Fatalf("sync status %v", sync)
	}
}

func TestStudentWorkspaceEndpoints(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.register("workspace@example.com")
	app := c.expect(201, "POST", "/api/applications", map[string]any{"programId": "gw-program"})
	id := app["id"].(string)
	testutil.Eventually(t, 5*time.Second, func() bool {
		res, _ := c.do("GET", "/api/applications/"+id+"/timeline", nil)
		return res.StatusCode == 200
	}, "timeline not created")

	app = c.expect(200, "PATCH", "/api/applications/"+id, map[string]any{"version": app["version"], "status": "in_progress", "round": "round_2", "notes": " draft SOP "})
	if app["status"] != "in_progress" || app["round"] != "round_2" || app["notes"] != "draft SOP" {
		t.Fatalf("patched %v", app)
	}
	c.expect(400, "PATCH", "/api/applications/"+id, map[string]any{"version": app["version"], "status": "accepted"})
	c.expect(400, "PATCH", "/api/applications/"+id, map[string]any{"version": app["version"], "round": "round_7"})
	c.expect(400, "PATCH", "/api/applications/"+id, map[string]any{"version": app["version"], "checklist": []any{map[string]any{"document": "passport", "done": true}}})
	c.expect(400, "PATCH", "/api/applications/"+id, map[string]any{"version": app["version"], "round": "final"})
	got := c.expect(200, "GET", "/api/applications/"+id, nil)
	if got["version"] != app["version"] {
		t.Fatal("rejected updates must not change the application")
	}
	list := c.expect(200, "GET", "/api/applications", nil)
	if len(list["applications"].([]any)) != 1 {
		t.Fatalf("list %v", list)
	}

	tl := c.expect(201, "POST", "/api/applications/"+id+"/milestones", map[string]any{"title": "Book IELTS", "dueDate": "2026-12-05", "description": "test centre downtown"})
	var custom string
	for _, m := range tl["milestones"].([]any) {
		if mm := m.(map[string]any); mm["source"] == "custom" {
			custom = mm["id"].(string)
		}
	}
	c.expect(400, "POST", "/api/applications/"+id+"/milestones", map[string]any{"title": "", "dueDate": "2026-12-05"})
	c.expect(400, "POST", "/api/applications/"+id+"/milestones", map[string]any{"title": "x", "dueDate": "12/05/2026"})
	tl = c.expect(200, "PATCH", "/api/applications/"+id+"/milestones/"+custom, map[string]any{"done": true, "title": "Book IELTS (done)"})
	c.expect(404, "PATCH", "/api/applications/"+id+"/milestones/c:missing", map[string]any{"done": true})
	c.expect(200, "DELETE", "/api/applications/"+id+"/milestones/"+custom, nil)

	tls := c.expect(200, "GET", "/api/timelines", nil)
	if len(tls["timelines"].([]any)) != 1 {
		t.Fatalf("timelines %v", tls)
	}
	ag := c.expect(200, "GET", "/api/agenda?from=2026-01-01&to=2026-12-31&includeDone=true", nil)
	if _, ok := ag["items"].([]any); !ok {
		t.Fatalf("agenda %v", ag)
	}
	c.expect(400, "GET", "/api/agenda?from=2026-13-01", nil)

	// Recommendation link management.
	rec := c.expect(201, "POST", "/api/applications/"+id+"/recommendations", map[string]any{"name": "Prof. Ade", "email": "ade@example.edu", "relationship": "Lecturer"})
	rid := rec["id"].(string)
	c.expect(409, "POST", "/api/applications/"+id+"/recommendations", map[string]any{"name": "Again", "email": "ADE@example.edu"})
	c.expect(400, "POST", "/api/applications/"+id+"/recommendations", map[string]any{"name": "No email", "email": "nope"})
	fresh := c.expect(200, "POST", "/api/applications/"+id+"/recommendations/"+rid+"/regenerate", map[string]any{})
	oldToken := strings.SplitN(rec["inviteUrl"].(string), "#token=", 2)[1]
	newToken := strings.SplitN(fresh["inviteUrl"].(string), "#token=", 2)[1]
	anon := e.client()
	anon.expect(404, "POST", "/api/invites/lookup", map[string]any{"token": oldToken})
	anon.expect(200, "POST", "/api/invites/decline", map[string]any{"token": newToken, "reason": "Travelling"})
	anon.expect(409, "POST", "/api/invites/decline", map[string]any{"token": newToken})
	c.expect(409, "POST", "/api/applications/"+id+"/recommendations/"+rid+"/cancel", map[string]any{})

	rec2 := c.expect(201, "POST", "/api/applications/"+id+"/recommendations", map[string]any{"name": "Dr. Bo", "email": "bo@example.edu"})
	cancelled := c.expect(200, "POST", "/api/applications/"+id+"/recommendations/"+rec2["id"].(string)+"/cancel", map[string]any{})
	if cancelled["status"] != "cancelled" {
		t.Fatalf("cancel %v", cancelled)
	}
	recs := c.expect(200, "GET", "/api/applications/"+id+"/recommendations", nil)["recommendations"].([]any)
	if len(recs) != 2 || recs[0].(map[string]any)["declineReason"] != "Travelling" {
		t.Fatalf("recs %v", recs)
	}

	// Form-encoded logout redirects home.
	res, _ := c.do("POST", "/api/auth/logout", nil, "Content-Type", "application/x-www-form-urlencoded")
	if res.StatusCode != 303 || res.Header.Get("Location") != "/" {
		t.Fatalf("form logout %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
