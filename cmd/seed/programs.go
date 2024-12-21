package main

import (
	"fmt"
	"time"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
)

// The sample catalog is fictional: universities, programs and deadlines are
// invented for demonstration, and websites use the reserved example.edu
// domain. Deadlines are placed relative to the next intake so the demo never
// shows a catalog full of passed dates.

type round struct {
	kind  catalogv1.RoundKind
	month time.Month
	day   int
	// yearOffset is relative to the intake year (-1 = the autumn before).
	yearOffset int
}

type sample struct {
	id, university, name, field, country, city, tz string
	degree                                         catalogv1.Degree
	rounds                                         []round
	toefl, ielts, letters                          int32
	gre                                            bool
	docs                                           []catalogv1.DocumentType
	feeCents                                       int64
	currency                                       string
	tags                                           []string
}

const (
	sop        = catalogv1.DocumentType_DOCUMENT_TYPE_STATEMENT_OF_PURPOSE
	cv         = catalogv1.DocumentType_DOCUMENT_TYPE_CV
	transcript = catalogv1.DocumentType_DOCUMENT_TYPE_TRANSCRIPT
	writing    = catalogv1.DocumentType_DOCUMENT_TYPE_WRITING_SAMPLE
	portfolio  = catalogv1.DocumentType_DOCUMENT_TYPE_PORTFOLIO

	master   = catalogv1.Degree_DEGREE_MASTER
	phd      = catalogv1.Degree_DEGREE_PHD
	mba      = catalogv1.Degree_DEGREE_MBA
	bachelor = catalogv1.Degree_DEGREE_BACHELOR

	priority = catalogv1.RoundKind_ROUND_KIND_PRIORITY
	r1       = catalogv1.RoundKind_ROUND_KIND_ROUND_1
	r2       = catalogv1.RoundKind_ROUND_KIND_ROUND_2
	r3       = catalogv1.RoundKind_ROUND_KIND_ROUND_3
	final    = catalogv1.RoundKind_ROUND_KIND_FINAL
)

var (
	usRounds   = []round{{r1, time.December, 1, -1}, {r2, time.January, 15, 0}}
	mbaRounds  = []round{{r1, time.October, 1, -1}, {r2, time.January, 8, 0}, {r3, time.April, 1, 0}}
	ukRounds   = []round{{priority, time.January, 10, 0}, {final, time.May, 31, 0}}
	euRounds   = []round{{r1, time.January, 15, 0}, {final, time.April, 15, 0}}
	asiaRounds = []round{{r1, time.December, 15, -1}, {r2, time.March, 1, 0}}
	auRounds   = []round{{r1, time.March, 31, 0}, {final, time.May, 31, 0}}
	phdRounds  = []round{{final, time.December, 15, -1}}
	std        = []catalogv1.DocumentType{sop, cv, transcript}
)

var samples = []sample{
	{"northbridge-ms-cs", "Northbridge University", "MS in Computer Science", "Computer Science", "United States", "Boston", "America/New_York", master, usRounds, 100, 70, 3, false, std, 9000, "USD", []string{"stem", "ai"}},
	{"northbridge-ms-ds", "Northbridge University", "MS in Data Science", "Data Science", "United States", "Boston", "America/New_York", master, usRounds, 100, 70, 2, true, std, 9000, "USD", []string{"stem", "statistics"}},
	{"northbridge-mba", "Northbridge University", "Full-time MBA", "Business", "United States", "Boston", "America/New_York", mba, mbaRounds, 105, 75, 2, false, []catalogv1.DocumentType{sop, cv, transcript}, 25000, "USD", []string{"business"}},
	{"pacific-coast-meng-ee", "Pacific Coast Institute of Technology", "MEng in Electrical Engineering", "Electrical Engineering", "United States", "San Diego", "America/Los_Angeles", master, usRounds, 90, 70, 2, false, std, 12000, "USD", []string{"stem", "hardware"}},
	{"pacific-coast-phd-robotics", "Pacific Coast Institute of Technology", "PhD in Robotics", "Robotics", "United States", "San Diego", "America/Los_Angeles", phd, phdRounds, 100, 70, 3, false, []catalogv1.DocumentType{sop, cv, transcript, writing}, 12000, "USD", []string{"stem", "research"}},
	{"lakeshore-ms-stats", "Lakeshore State University", "MS in Statistics", "Statistics", "United States", "Chicago", "America/Chicago", master, usRounds, 80, 65, 2, true, std, 7500, "USD", []string{"stem"}},
	{"lakeshore-mpp", "Lakeshore State University", "Master of Public Policy", "Public Policy", "United States", "Chicago", "America/Chicago", master, usRounds, 100, 70, 2, false, []catalogv1.DocumentType{sop, cv, transcript, writing}, 7500, "USD", []string{"policy"}},
	{"summit-ms-finance", "Summit School of Business", "MS in Finance", "Finance", "United States", "Denver", "America/Denver", master, mbaRounds, 100, 70, 1, true, std, 15000, "USD", []string{"business", "finance"}},
	{"summit-mba", "Summit School of Business", "Global MBA", "Business", "United States", "Denver", "America/Denver", mba, mbaRounds, 100, 70, 2, false, std, 20000, "USD", []string{"business"}},
	{"harborview-bs-cs", "Harborview College", "BS in Computer Science (transfer)", "Computer Science", "United States", "Seattle", "America/Los_Angeles", bachelor, []round{{r1, time.February, 15, 0}}, 80, 65, 1, false, []catalogv1.DocumentType{sop, transcript}, 6000, "USD", []string{"undergraduate"}},

	{"maple-ridge-msc-ai", "Maple Ridge University", "MSc in Artificial Intelligence", "Computer Science", "Canada", "Toronto", "America/Toronto", master, usRounds, 93, 70, 2, false, std, 12500, "CAD", []string{"stem", "ai"}},
	{"maple-ridge-mmath", "Maple Ridge University", "MMath in Combinatorics and Optimization", "Mathematics", "Canada", "Toronto", "America/Toronto", master, usRounds, 90, 70, 3, false, std, 12500, "CAD", []string{"stem", "math"}},
	{"maple-ridge-phd-cs", "Maple Ridge University", "PhD in Computer Science", "Computer Science", "Canada", "Toronto", "America/Toronto", phd, phdRounds, 93, 70, 3, false, []catalogv1.DocumentType{sop, cv, transcript, writing}, 12500, "CAD", []string{"research"}},
	{"west-coast-mds", "University of the West Coast", "Master of Data Science", "Data Science", "Canada", "Vancouver", "America/Vancouver", master, []round{{r1, time.November, 30, -1}, {r2, time.February, 1, 0}}, 90, 65, 2, false, std, 16800, "CAD", []string{"stem"}},
	{"st-laurent-meng", "St. Laurent Polytechnic", "MEng in Software Engineering", "Software Engineering", "Canada", "Montreal", "America/Toronto", master, euRounds, 90, 65, 2, false, std, 10500, "CAD", []string{"stem", "french-friendly"}},

	{"thamesbury-msc-ml", "Thamesbury College London", "MSc Machine Learning", "Computer Science", "United Kingdom", "London", "Europe/London", master, ukRounds, 100, 70, 2, false, std, 9000, "GBP", []string{"stem", "ai"}},
	{"thamesbury-msc-finance", "Thamesbury College London", "MSc Finance", "Finance", "United Kingdom", "London", "Europe/London", master, ukRounds, 100, 70, 1, false, std, 9000, "GBP", []string{"business", "finance"}},
	{"highmoor-mphil-econ", "Highmoor University", "MPhil in Economics", "Economics", "United Kingdom", "Edinburgh", "Europe/London", master, ukRounds, 110, 75, 3, true, []catalogv1.DocumentType{sop, cv, transcript, writing}, 7500, "GBP", []string{"research"}},
	{"highmoor-ma-design", "Highmoor University", "MA Interaction Design", "Design", "United Kingdom", "Edinburgh", "Europe/London", master, ukRounds, 92, 65, 2, false, []catalogv1.DocumentType{sop, cv, transcript, portfolio}, 6000, "GBP", []string{"design", "portfolio"}},
	{"riverside-msc-public-health", "Riverside University", "MSc Global Public Health", "Public Health", "United Kingdom", "Manchester", "Europe/London", master, ukRounds, 90, 65, 2, false, std, 0, "", []string{"health"}},

	{"rheinhafen-msc-informatics", "Rheinhafen Technical University", "MSc Informatics", "Computer Science", "Germany", "Cologne", "Europe/Berlin", master, euRounds, 88, 65, 2, true, std, 0, "", []string{"stem", "no-tuition"}},
	{"rheinhafen-msc-mech", "Rheinhafen Technical University", "MSc Mechanical Engineering", "Mechanical Engineering", "Germany", "Cologne", "Europe/Berlin", master, euRounds, 88, 65, 1, false, std, 0, "", []string{"stem", "no-tuition"}},
	{"isarberg-msc-management", "Isarberg School of Management", "MSc Management", "Business", "Germany", "Munich", "Europe/Berlin", master, []round{{r1, time.November, 15, -1}, {r2, time.January, 31, 0}, {r3, time.March, 31, 0}}, 95, 70, 1, false, std, 12000, "EUR", []string{"business"}},
	{"delta-msc-water", "Delta University of Technology", "MSc Water Management", "Civil Engineering", "Netherlands", "Delft", "Europe/Amsterdam", master, euRounds, 90, 65, 2, false, std, 10000, "EUR", []string{"stem", "sustainability"}},
	{"delta-msc-cs", "Delta University of Technology", "MSc Computer Science", "Computer Science", "Netherlands", "Delft", "Europe/Amsterdam", master, euRounds, 90, 65, 2, false, std, 10000, "EUR", []string{"stem"}},
	{"lumiere-msc-ds", "École Lumière", "MSc Data Science for Business", "Data Science", "France", "Paris", "Europe/Paris", master, []round{{r1, time.October, 20, -1}, {r2, time.December, 15, -1}, {r3, time.February, 28, 0}}, 95, 70, 2, true, std, 18000, "EUR", []string{"business", "stem"}},
	{"alpine-msc-robotics", "Alpine Institute of Technology", "MSc Robotics, Systems and Control", "Robotics", "Switzerland", "Zurich", "Europe/Zurich", master, []round{{final, time.December, 15, -1}}, 100, 70, 2, true, std, 15000, "CHF", []string{"stem", "research"}},
	{"alpine-phd-physics", "Alpine Institute of Technology", "PhD in Physics", "Physics", "Switzerland", "Zurich", "Europe/Zurich", phd, []round{{r1, time.November, 30, -1}, {r2, time.March, 31, 0}}, 100, 70, 3, false, []catalogv1.DocumentType{sop, cv, transcript, writing}, 15000, "CHF", []string{"research"}},
	{"nordvik-msc-sustainable", "Nordvik University", "MSc Sustainable Energy", "Energy Engineering", "Sweden", "Stockholm", "Europe/Stockholm", master, []round{{final, time.January, 15, 0}}, 90, 65, 2, false, std, 90000, "SEK", []string{"stem", "sustainability"}},

	{"sakura-meng-is", "Sakura Institute of Science", "MEng in Information Science", "Computer Science", "Japan", "Tokyo", "Asia/Tokyo", master, asiaRounds, 80, 65, 2, false, std, 3000000, "JPY", []string{"stem"}},
	{"sakura-mba", "Sakura Institute of Science", "MBA in Global Leadership", "Business", "Japan", "Tokyo", "Asia/Tokyo", mba, asiaRounds, 90, 65, 2, false, std, 3000000, "JPY", []string{"business"}},
	{"lion-city-msc-fintech", "Lion City University", "MSc in Financial Technology", "Finance", "Singapore", "Singapore", "Asia/Singapore", master, asiaRounds, 90, 65, 2, false, std, 10000, "SGD", []string{"business", "stem"}},
	{"lion-city-msc-ai", "Lion City University", "MSc in Artificial Intelligence", "Computer Science", "Singapore", "Singapore", "Asia/Singapore", master, asiaRounds, 92, 65, 2, true, std, 10000, "SGD", []string{"stem", "ai"}},
	{"harbour-mphil-eng", "Harbour University", "MPhil in Engineering", "Mechanical Engineering", "Hong Kong", "Hong Kong", "Asia/Hong_Kong", master, asiaRounds, 80, 65, 2, false, std, 30000, "HKD", []string{"research"}},
	{"hanriver-ms-semis", "Han River Institute of Technology", "MS in Semiconductor Engineering", "Electrical Engineering", "South Korea", "Seoul", "Asia/Seoul", master, asiaRounds, 85, 65, 2, false, std, 8000000, "KRW", []string{"stem", "hardware"}},

	{"southern-cross-mit", "Southern Cross University of Technology", "Master of Information Technology", "Computer Science", "Australia", "Sydney", "Australia/Sydney", master, auRounds, 79, 65, 0, false, []catalogv1.DocumentType{cv, transcript}, 15000, "AUD", []string{"stem", "coursework"}},
	{"southern-cross-mba", "Southern Cross University of Technology", "MBA", "Business", "Australia", "Sydney", "Australia/Sydney", mba, auRounds, 90, 70, 2, false, std, 15000, "AUD", []string{"business"}},
	{"kauri-mph", "Kauri University", "Master of Public Health", "Public Health", "New Zealand", "Auckland", "Pacific/Auckland", master, auRounds, 90, 65, 1, false, std, 10000, "NZD", []string{"health"}},
}

// intakeYear is the first autumn intake for which the program still has a
// round open today (deadlines fall from the autumn before the intake to the
// spring of the intake year, so the month alone cannot tell).
func intakeYear(s sample, now time.Time) int {
	if len(s.rounds) == 0 {
		return now.Year() + 1
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for y := now.Year(); ; y++ {
		for _, r := range s.rounds {
			if !time.Date(y+r.yearOffset, r.month, r.day, 0, 0, 0, 0, time.UTC).Before(today) {
				return y
			}
		}
	}
}

func toProgram(s sample, now time.Time) *catalogv1.Program {
	y := intakeYear(s, now)
	p := &catalogv1.Program{
		Id: s.id, University: s.university, Name: s.name, Degree: s.degree, Field: s.field,
		Country: s.country, City: s.city, Timezone: s.tz, Intake: fmt.Sprintf("Fall %d", y),
		Requirements: &catalogv1.Requirements{
			MinToefl: s.toefl, MinIeltsTenths: s.ielts, GreRequired: s.gre,
			RecommendationsRequired: s.letters, Documents: s.docs,
		},
		ApplicationFeeCents: s.feeCents, Currency: s.currency, Tags: s.tags,
		Website: "https://www.example.edu/" + s.id,
	}
	for _, r := range s.rounds {
		d := time.Date(y+r.yearOffset, r.month, r.day, 0, 0, 0, 0, time.UTC)
		p.Deadlines = append(p.Deadlines, &catalogv1.Deadline{Round: r.kind, Date: d.Format("2006-01-02")})
	}
	return p
}

func samplePrograms(now time.Time) []*catalogv1.Program {
	out := make([]*catalogv1.Program, 0, len(samples))
	for _, s := range samples {
		out = append(out, toProgram(s, now))
	}
	return out
}

var fields = []string{"Computer Science", "Data Science", "Economics", "Finance", "Mechanical Engineering", "Public Health", "Design", "Physics"}
var countries = []struct{ name, city, tz string }{
	{"United States", "Austin", "America/Chicago"}, {"Canada", "Ottawa", "America/Toronto"},
	{"United Kingdom", "Bristol", "Europe/London"}, {"Germany", "Hamburg", "Europe/Berlin"},
	{"Japan", "Osaka", "Asia/Tokyo"}, {"Australia", "Melbourne", "Australia/Melbourne"},
}

// syntheticProgram generates load-test data ("Synthetic University 0042").
func syntheticProgram(i int, now time.Time) *catalogv1.Program {
	c := countries[i%len(countries)]
	f := fields[i%len(fields)]
	s := sample{
		id: fmt.Sprintf("synthetic-%05d", i), university: fmt.Sprintf("Synthetic University %04d", i/4),
		name: fmt.Sprintf("Master of %s %d", f, i%4), field: f, country: c.name, city: c.city, tz: c.tz,
		degree: master, rounds: usRounds, toefl: 90, ielts: 65, letters: 2, docs: std, feeCents: 5000, currency: "USD",
		tags: []string{"synthetic"},
	}
	return toProgram(s, now)
}
