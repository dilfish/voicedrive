package groupresolve

import (
	"testing"
	"time"

	"github.com/Study-of-slag-weng/voicedrive1/internal/model"
)

func candidate(id, name string, aliases []string, created string, members int) Candidate {
	when, _ := time.Parse(time.RFC3339, created)
	return Candidate{Group: model.Group{ID: id, Name: name, CreatedAt: when}, Aliases: aliases, Members: members}
}

func TestResolveCases(t *testing.T) {
	cases := []struct {
		name, query, status, id string
		options                 int
	}{
		{"exact", "夜班组", StatusMatched, "g1", 0},
		{"suffix and spaces", " 夜 班 群 ", StatusMatched, "g1", 0},
		{"alias", "值班", StatusMatched, "g1", 0},
		{"contains", "夜班", StatusMatched, "g1", 0},
		{"typo", "夜班祖", StatusMatched, "g1", 0},
		{"ambiguous", "夜班", StatusAmbiguous, "", 2},
		{"missing", "港口调度", StatusNotFound, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := []Candidate{candidate("g1", "夜班组", []string{"值班"}, "2026-01-01T00:00:00Z", 3)}
			if tc.name == "ambiguous" {
				cs = append(cs, candidate("g2", "夜班群", nil, "2026-01-02T00:00:00Z", 5))
			}
			got := Resolve(tc.query, cs)
			if got.Status != tc.status || got.Group.ID != tc.id || len(got.Options) != tc.options {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestResolveCapsAmbiguousOptions(t *testing.T) {
	cs := []Candidate{candidate("1", "值班一组", []string{"值班"}, "2026-01-01T00:00:00Z", 1), candidate("2", "值班二组", []string{"值班"}, "2026-01-02T00:00:00Z", 1), candidate("3", "值班三组", []string{"值班"}, "2026-01-03T00:00:00Z", 1), candidate("4", "值班四组", []string{"值班"}, "2026-01-04T00:00:00Z", 1)}
	got := Resolve("值班", cs)
	if got.Status != StatusAmbiguous || len(got.Options) != 3 {
		t.Fatalf("got %+v", got)
	}
}
