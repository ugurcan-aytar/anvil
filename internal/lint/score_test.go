package lint

import "testing"

func TestHealthScoreEmptyReport(t *testing.T) {
	if got := HealthScore(&LintReport{}); got != MaxHealthScore {
		t.Errorf("empty report → want %d, got %v", MaxHealthScore, got)
	}
}

func TestHealthScoreNilReport(t *testing.T) {
	if got := HealthScore(nil); got != MaxHealthScore {
		t.Errorf("nil report → want %d, got %v", MaxHealthScore, got)
	}
}

func TestHealthScoreWeightsEachBucket(t *testing.T) {
	cases := []struct {
		name   string
		report LintReport
		want   float64
	}{
		{"one orphan", LintReport{Orphans: []string{"a"}}, 98},
		{"one missing page", LintReport{MissingPages: []string{"a"}}, 97},
		{"one contradiction", LintReport{Contradictions: []Contradiction{{}}}, 95},
		{"one stale claim", LintReport{StaleClaims: []StaleClaim{{}}}, 97},
		{"one empty page", LintReport{EmptyPages: []string{"a"}}, 98},
		{"one broken link", LintReport{BrokenLinks: []BrokenLink{{}}}, 98},
		{"one stale index", LintReport{StaleIndex: []string{"a"}}, 99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HealthScore(&tc.report); got != tc.want {
				t.Errorf("score = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHealthScoreClampsAtZero(t *testing.T) {
	// Massive penalties push below zero — clamp should hold.
	report := &LintReport{
		Orphans:        make([]string, 100),
		MissingPages:   make([]string, 100),
		BrokenLinks:    make([]BrokenLink, 100),
		EmptyPages:     make([]string, 100),
		StaleIndex:     make([]string, 100),
		Contradictions: make([]Contradiction, 100),
		StaleClaims:    make([]StaleClaim, 100),
	}
	if got := HealthScore(report); got != 0 {
		t.Errorf("heavily-degraded score should clamp to 0, got %v", got)
	}
}

func TestHealthScoreOrderingMatchesWeights(t *testing.T) {
	// A wiki with the same count of each issue type should see
	// stronger penalties hit contradictions/stale hardest. Verifies
	// the weight ordering didn't drift.
	base := &LintReport{}
	withContra := &LintReport{Contradictions: []Contradiction{{}}}
	withOrphan := &LintReport{Orphans: []string{"a"}}

	if HealthScore(withOrphan) <= HealthScore(withContra) {
		t.Errorf("a single orphan should hurt less than a single contradiction: orphan=%v contra=%v",
			HealthScore(withOrphan), HealthScore(withContra))
	}
	if HealthScore(base) <= HealthScore(withContra) {
		t.Errorf("base should outscore any non-empty report")
	}
}
