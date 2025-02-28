package gateway

import (
	"bytes"
	"io/fs"
	"net/http"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/xmlexport"
)

// writeXML renders into a buffer first, so an encoding error becomes a clean
// 500 instead of a truncated download.
func (s *Server) writeXML(w http.ResponseWriter, r *http.Request, filename string, render func(*bytes.Buffer) error) {
	var buf bytes.Buffer
	if err := render(&buf); err != nil {
		s.log.ErrorContext(r.Context(), "xml export failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "export failed")
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) studentName(r *http.Request) string {
	if a, ok := accountFrom(r.Context()); ok {
		return a.Name
	}
	return ""
}

func (s *Server) timelineXML(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	t, err := s.c.Timeline.GetTimeline(ctx, &timelinev1.GetTimelineRequest{ApplicationId: r.PathValue("id"), Consistent: true})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	s.writeXML(w, r, "timeline-"+t.ApplicationId+".xml", func(b *bytes.Buffer) error {
		return xmlexport.WriteTimelines(b, s.studentName(r), []*timelinev1.Timeline{t}, time.Now())
	})
}

func (s *Server) allTimelinesXML(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	res, err := s.c.Timeline.ListTimelines(ctx, &timelinev1.ListTimelinesRequest{Consistent: true}) // exports are rare; never stale
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	s.writeXML(w, r, "timelines.xml", func(b *bytes.Buffer) error {
		return xmlexport.WriteTimelines(b, s.studentName(r), res.Timelines, time.Now())
	})
}

func (s *Server) packetXML(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.backend(r)
	defer cancel()
	p, err := s.c.Apps.GetRecommendationPacket(ctx, &appsv1.GetRecommendationPacketRequest{ApplicationId: r.PathValue("id")})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	s.writeXML(w, r, "recommendation-packet-"+p.Application.Id+".xml", func(b *bytes.Buffer) error {
		return xmlexport.WritePacket(b, p)
	})
}

func (s *Server) schema(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(xmlexport.Schemas, "schemas/"+r.PathValue("file"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}
