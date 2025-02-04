package gateway

import (
	"net/http"
	"strconv"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
)

func bad(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "invalid_argument", msg)
}

// --- catalog ---------------------------------------------------------------

func (s *Server) listPrograms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	deg, err := parseDegree(q.Get("degree"))
	if err != nil {
		bad(w, "unknown degree")
		return
	}
	size, _ := strconv.Atoi(q.Get("pageSize"))
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Catalog.ListPrograms(ctx, &catalogv1.ListProgramsRequest{
		Query: q.Get("q"), Country: q.Get("country"), Field: q.Get("field"), Degree: deg,
		PageSize: int32(size), PageToken: q.Get("pageToken"),
	})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	out := make([]programDTO, 0, len(res.Programs))
	for _, p := range res.Programs {
		out = append(out, programOut(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"programs": out, "nextPageToken": res.NextPageToken})
}

func (s *Server) facets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Catalog.ListFacets(ctx, &catalogv1.ListFacetsRequest{})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"countries": nonNil(res.Countries), "fields": nonNil(res.Fields)})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) getProgram(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	p, err := s.c.Catalog.GetProgram(ctx, &catalogv1.GetProgramRequest{Id: r.PathValue("id")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, programOut(p))
}

func (s *Server) createProgram(w http.ResponseWriter, r *http.Request) {
	var in programDTO
	if !readJSON(w, r, &in) {
		return
	}
	p, err := programIn(in)
	if err != nil {
		bad(w, err.Error())
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	out, err := s.c.Catalog.CreateProgram(ctx, &catalogv1.CreateProgramRequest{Program: p})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, programOut(out))
}

func (s *Server) updateProgram(w http.ResponseWriter, r *http.Request) {
	var in programDTO
	if !readJSON(w, r, &in) {
		return
	}
	p, err := programIn(in)
	if err != nil {
		bad(w, err.Error())
		return
	}
	p.Id = r.PathValue("id")
	ctx, cancel := s.backend(r)
	defer cancel()
	out, err := s.c.Catalog.UpdateProgram(ctx, &catalogv1.UpdateProgramRequest{Program: p, ExpectedVersion: in.Version})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, programOut(out))
}

func (s *Server) syncStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Timeline.GetSyncStatus(ctx, &timelinev1.GetSyncStatusRequest{})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	type stream struct {
		Stream      string `json:"stream"`
		Cursor      int64  `json:"cursor"`
		Head        int64  `json:"head"`
		Lag         int64  `json:"lag"`
		Connected   bool   `json:"connected"`
		Leader      bool   `json:"leader"`
		Applied     int64  `json:"applied"`
		LastEventAt string `json:"lastEventAt,omitempty"`
	}
	out := []stream{}
	for _, st := range res.Streams {
		out = append(out, stream{st.Stream, st.Cursor, st.HeadSeq, max(st.HeadSeq-st.Cursor, 0), st.Connected, st.Leader, st.EventsApplied, ts(st.LastEventAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"streams": out})
}

// --- applications ----------------------------------------------------------

func (s *Server) listApplications(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Apps.ListApplications(ctx, &appsv1.ListApplicationsRequest{PageSize: 100, PageToken: r.URL.Query().Get("pageToken")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	out := make([]applicationDTO, 0, len(res.Applications))
	for _, a := range res.Applications {
		out = append(out, applicationOut(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": out, "nextPageToken": res.NextPageToken})
}

func (s *Server) createApplication(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProgramID string `json:"programId"`
		Round     string `json:"round"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	round, err := parseRound(in.Round)
	if err != nil {
		bad(w, "unknown round")
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	a, err := s.c.Apps.CreateApplication(ctx, &appsv1.CreateApplicationRequest{ProgramId: in.ProgramID, Round: round})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, applicationOut(a))
}

func (s *Server) getApplication(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	a, err := s.c.Apps.GetApplication(ctx, &appsv1.GetApplicationRequest{Id: r.PathValue("id")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, applicationOut(a))
}

func (s *Server) updateApplication(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version   int64   `json:"version"`
		Status    *string `json:"status"`
		Round     *string `json:"round"`
		Notes     *string `json:"notes"`
		Checklist []struct {
			Document string `json:"document"`
			Done     bool   `json:"done"`
		} `json:"checklist"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	req := &appsv1.UpdateApplicationRequest{Id: r.PathValue("id"), ExpectedVersion: in.Version, Notes: in.Notes}
	if in.Status != nil {
		st, err := parseStatus(*in.Status)
		if err != nil || st == 0 {
			bad(w, "unknown status")
			return
		}
		req.Status = st.Enum()
	}
	if in.Round != nil {
		rd, err := parseRound(*in.Round)
		if err != nil || rd == 0 {
			bad(w, "unknown round")
			return
		}
		req.Round = rd.Enum()
	}
	for _, c := range in.Checklist {
		d, err := parseDoc(c.Document)
		if err != nil || d == 0 {
			bad(w, "unknown document "+c.Document)
			return
		}
		req.Checklist = append(req.Checklist, &appsv1.ChecklistUpdate{Document: d, Done: c.Done})
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	a, err := s.c.Apps.UpdateApplication(ctx, req)
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, applicationOut(a))
}

func (s *Server) deleteApplication(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	if _, err := s.c.Apps.DeleteApplication(ctx, &appsv1.DeleteApplicationRequest{Id: r.PathValue("id")}); err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- timeline --------------------------------------------------------------

func (s *Server) getTimeline(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	t, err := s.c.Timeline.GetTimeline(ctx, &timelinev1.GetTimelineRequest{ApplicationId: r.PathValue("id")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, timelineOut(t))
}

func (s *Server) listTimelines(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Timeline.ListTimelines(ctx, &timelinev1.ListTimelinesRequest{})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	out := make([]timelineDTO, 0, len(res.Timelines))
	for _, t := range res.Timelines {
		out = append(out, timelineOut(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"timelines": out})
}

func (s *Server) agenda(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Timeline.GetAgenda(ctx, &timelinev1.GetAgendaRequest{From: q.Get("from"), To: q.Get("to"), IncludeDone: q.Get("includeDone") == "true"})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	out := make([]agendaItemDTO, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, agendaItemDTO{ApplicationID: it.ApplicationId, ProgramName: it.ProgramName, University: it.University, Milestone: milestoneOut(it.Milestone)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) addMilestone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		DueDate     string `json:"dueDate"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	t, err := s.c.Timeline.AddMilestone(ctx, &timelinev1.AddMilestoneRequest{ApplicationId: r.PathValue("id"), Title: in.Title, Description: in.Description, DueDate: in.DueDate})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, timelineOut(t))
}

func (s *Server) updateMilestone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Done        *bool   `json:"done"`
		Title       *string `json:"title"`
		Description *string `json:"description"`
		DueDate     *string `json:"dueDate"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	t, err := s.c.Timeline.UpdateMilestone(ctx, &timelinev1.UpdateMilestoneRequest{
		ApplicationId: r.PathValue("id"), MilestoneId: r.PathValue("mid"),
		Done: in.Done, Title: in.Title, Description: in.Description, DueDate: in.DueDate,
	})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, timelineOut(t))
}

func (s *Server) deleteMilestone(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	t, err := s.c.Timeline.DeleteMilestone(ctx, &timelinev1.DeleteMilestoneRequest{ApplicationId: r.PathValue("id"), MilestoneId: r.PathValue("mid")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, timelineOut(t))
}

// --- recommendations -------------------------------------------------------

func (s *Server) inviteURL(token string) string {
	// The token travels in the fragment, so it never reaches server logs,
	// proxies or Referer headers; the page posts it in a JSON body.
	return s.cfg.PublicURL + "/recommend.html#token=" + token
}

func (s *Server) listRecommendations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Recs.ListRecommendations(ctx, &appsv1.ListRecommendationsRequest{ApplicationId: r.PathValue("id")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	out := make([]recommendationDTO, 0, len(res.Recommendations))
	for _, rec := range res.Recommendations {
		out = append(out, recommendationOut(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"recommendations": out})
}

func (s *Server) requestRecommendation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string `json:"name"`
		Email        string `json:"email"`
		Relationship string `json:"relationship"`
		WaivedAccess bool   `json:"waivedAccess"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Recs.RequestRecommendation(ctx, &appsv1.RequestRecommendationRequest{
		ApplicationId: r.PathValue("id"), RecommenderName: in.Name, RecommenderEmail: in.Email,
		Relationship: in.Relationship, WaivedAccess: in.WaivedAccess,
	})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	out := recommendationOut(res.Recommendation)
	out.InviteURL = s.inviteURL(res.Token)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) cancelRecommendation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	rec, err := s.c.Recs.CancelRecommendation(ctx, &appsv1.CancelRecommendationRequest{ApplicationId: r.PathValue("id"), RecommendationId: r.PathValue("rid")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, recommendationOut(rec))
}

func (s *Server) regenerateLink(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Recs.RegenerateRecommendationLink(ctx, &appsv1.RegenerateRecommendationLinkRequest{ApplicationId: r.PathValue("id"), RecommendationId: r.PathValue("rid")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	out := recommendationOut(res.Recommendation)
	out.InviteURL = s.inviteURL(res.Token)
	writeJSON(w, http.StatusOK, out)
}

// --- recommender (token) endpoints ----------------------------------------

func (s *Server) inviteLookup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	inv, err := s.c.Recs.GetRecommendationInvite(ctx, &appsv1.GetRecommendationInviteRequest{Token: in.Token})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, inviteOut(inv))
}

func (s *Server) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token  string `json:"token"`
		Letter string `json:"letter"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	inv, err := s.c.Recs.SubmitRecommendation(ctx, &appsv1.SubmitRecommendationRequest{Token: in.Token, LetterText: in.Letter})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, inviteOut(inv))
}

func (s *Server) inviteDecline(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token  string `json:"token"`
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	inv, err := s.c.Recs.DeclineRecommendation(ctx, &appsv1.DeclineRecommendationRequest{Token: in.Token, Reason: in.Reason})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, inviteOut(inv))
}
