package lint

// Per-issue deductions for the health score. Weighted so
// contradictions and stale claims (content-level problems) hit
// harder than structural gaps (orphan / missing / empty) which are
// often just ingest-backlog rather than real wiki rot.
const (
	orphanPenalty        = 2
	missingPagePenalty   = 3
	contradictionPenalty = 5
	stalePenalty         = 3
	emptyPenalty         = 2
	brokenLinkPenalty    = 2
	staleIndexPenalty    = 1

	// MaxHealthScore is the "perfect wiki" ceiling. A fresh `anvil
	// init`-only project scores this because every check returns
	// zero findings.
	MaxHealthScore = 100
)

// HealthScore is a back-of-the-envelope wiki-quality number in
// [0, 100]. It's not a benchmark — the weights are tuned so that a
// few routine findings (a couple of orphans) still leave the user
// in the healthy 90s, while genuine rot (multiple contradictions +
// stale claims) pushes the number down to action territory.
func HealthScore(report *LintReport) float64 {
	if report == nil {
		return MaxHealthScore
	}
	score := float64(MaxHealthScore)
	score -= float64(len(report.Orphans)) * orphanPenalty
	score -= float64(len(report.MissingPages)) * missingPagePenalty
	score -= float64(len(report.BrokenLinks)) * brokenLinkPenalty
	score -= float64(len(report.EmptyPages)) * emptyPenalty
	score -= float64(len(report.StaleIndex)) * staleIndexPenalty
	score -= float64(len(report.Contradictions)) * contradictionPenalty
	score -= float64(len(report.StaleClaims)) * stalePenalty
	if score < 0 {
		return 0
	}
	if score > MaxHealthScore {
		return MaxHealthScore
	}
	return score
}
