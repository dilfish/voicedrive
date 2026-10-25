// Package groupresolve resolves spoken group names without letting the model
// choose a database identifier.
package groupresolve

import (
	"sort"
	"strings"
	"unicode"

	"github.com/Study-of-slag-weng/voicedrive1/internal/model"
)

const (
	StatusMatched   = "matched"
	StatusAmbiguous = "ambiguous"
	StatusNotFound  = "not_found"
)

type Candidate struct {
	Group   model.Group
	Aliases []string
	Members int
}

type Resolution struct {
	Status  string
	Group   model.Group
	Options []model.Group
}

func Normalize(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(r)
	}
	result := b.String()
	result = strings.TrimSuffix(result, "群")
	return result
}

func Resolve(query string, candidates []Candidate) Resolution {
	needle := Normalize(query)
	if needle == "" {
		return Resolution{Status: StatusNotFound}
	}
	type scored struct {
		candidate Candidate
		score     int
	}
	matched := make([]scored, 0)
	for _, candidate := range candidates {
		score := bestScore(needle, candidate)
		if score >= 0 {
			matched = append(matched, scored{candidate, score})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score < matched[j].score
		}
		if !matched[i].candidate.Group.CreatedAt.Equal(matched[j].candidate.Group.CreatedAt) {
			return matched[i].candidate.Group.CreatedAt.Before(matched[j].candidate.Group.CreatedAt)
		}
		return matched[i].candidate.Members > matched[j].candidate.Members
	})
	groups := make([]model.Group, 0, len(matched))
	seen := map[string]bool{}
	for _, item := range matched {
		if seen[item.candidate.Group.ID] {
			continue
		}
		seen[item.candidate.Group.ID] = true
		groups = append(groups, item.candidate.Group)
	}
	if len(groups) == 0 {
		return Resolution{Status: StatusNotFound}
	}
	if len(groups) == 1 {
		return Resolution{Status: StatusMatched, Group: groups[0]}
	}
	if len(groups) > 3 {
		groups = groups[:3]
	}
	return Resolution{Status: StatusAmbiguous, Options: groups}
}

func bestScore(needle string, candidate Candidate) int {
	values := append([]string{candidate.Group.Name}, candidate.Aliases...)
	best := -1
	for _, value := range values {
		name := Normalize(value)
		if name == "" {
			continue
		}
		score := 3
		switch {
		case name == needle:
			score = 0
		case strings.Contains(name, needle) || strings.Contains(needle, name):
			score = 1
		case levenshtein(name, needle) <= 2:
			score = 2
		default:
			continue
		}
		if best < 0 || score < best {
			best = score
		}
	}
	return best
}

func levenshtein(a, b string) int {
	left, right := []rune(a), []rune(b)
	if len(left) < len(right) {
		left, right = right, left
	}
	previous := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, x := range left {
		current := make([]int, len(right)+1)
		current[0] = i + 1
		for j, y := range right {
			cost := 0
			if x != y {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}

func min(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}
