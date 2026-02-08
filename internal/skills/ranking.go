package skills

import (
	"math"
	"sort"
	"strings"
	"time"
)

type UsageStats struct {
	Count    int       `json:"count"`
	LastUsed time.Time `json:"lastUsed"`
}

type Ranker struct {
	HalfLifeDays           float64
	RecentBoost            float64
	AdaptiveLearningWeight float64
	InputMatchBoost        float64
	AliasMatchBoost        float64
	BaseCategoryBoost      map[string]float64
}

func DefaultRanker() *Ranker {
	return &Ranker{
		HalfLifeDays:           14,
		RecentBoost:            250,
		AdaptiveLearningWeight: 400,
		InputMatchBoost:        600,
		AliasMatchBoost:        500,
		BaseCategoryBoost: map[string]float64{
			"convert":   800,
			"transform": 700,
			"compress":  650,
			"filter":    600,
			"meta":      400,
		},
	}
}

func (r *Ranker) Rank(skills []Skill, query string, inputTypes []string, usage map[string]UsageStats) []Skill {
	type scored struct {
		skill Skill
		score float64
	}
	scoredSkills := make([]scored, 0, len(skills))
	for _, skill := range skills {
		score := r.scoreSkill(skill, query, inputTypes, usage)
		scoredSkills = append(scoredSkills, scored{skill: skill, score: score})
	}
	sort.SliceStable(scoredSkills, func(i, j int) bool {
		if scoredSkills[i].score == scoredSkills[j].score {
			return scoredSkills[i].skill.Name < scoredSkills[j].skill.Name
		}
		return scoredSkills[i].score > scoredSkills[j].score
	})
	ranked := make([]Skill, 0, len(scoredSkills))
	for _, item := range scoredSkills {
		ranked = append(ranked, item.skill)
	}
	return ranked
}

func (r *Ranker) scoreSkill(skill Skill, query string, inputTypes []string, usage map[string]UsageStats) float64 {
	base := r.BaseCategoryBoost[skill.Category]
	if skill.IsMeta {
		base += 150
	}

	matchScore := 0.0
	if strings.TrimSpace(query) != "" {
		matchScore = fuzzyScore(skill.Name, query)
		if aliasMatch(skill.Aliases, query) {
			matchScore += r.AliasMatchBoost
		}
	}

	if inputMatches(skill, inputTypes) {
		matchScore += r.InputMatchBoost
	}

	adaptive := r.frecencyBoost(skill.ID, usage)
	return base + matchScore + adaptive
}

func (r *Ranker) frecencyBoost(skillID string, usage map[string]UsageStats) float64 {
	stat, ok := usage[skillID]
	if !ok {
		return 0
	}
	if stat.Count == 0 {
		return 0
	}
	ageDays := time.Since(stat.LastUsed).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	lambda := math.Log(2) / r.HalfLifeDays
	freq := float64(stat.Count)
	decay := math.Exp(-lambda * ageDays)
	recent := 0.0
	if ageDays < 1 {
		recent = r.RecentBoost
	}
	return (freq * r.AdaptiveLearningWeight * decay) + recent
}

func inputMatches(skill Skill, inputTypes []string) bool {
	if len(skill.InputTypes) == 0 {
		return len(inputTypes) == 0
	}
	for _, t := range skill.InputTypes {
		if t == "*" {
			return true
		}
	}
	if len(inputTypes) == 0 {
		return false
	}
	for _, t := range inputTypes {
		for _, supported := range skill.InputTypes {
			if strings.EqualFold(t, supported) {
				return true
			}
		}
	}
	return false
}

func aliasMatch(aliases []string, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	for _, alias := range aliases {
		a := strings.ToLower(alias)
		if a == q || strings.HasPrefix(a, q) {
			return true
		}
	}
	return false
}

func fuzzyScore(text string, query string) float64 {
	if text == "" || query == "" {
		return 0
	}
	t := strings.ToLower(text)
	q := strings.ToLower(query)
	if t == q {
		return 900
	}
	if strings.HasPrefix(t, q) {
		return 700
	}
	if strings.Contains(t, q) {
		return 450
	}
	dist := levenshtein(t, q)
	maxLen := float64(max(len(t), len(q)))
	if maxLen == 0 {
		return 0
	}
	sim := 1 - (float64(dist) / maxLen)
	if sim < 0 {
		sim = 0
	}
	return sim * 400
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = min(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		copy(prev, curr)
	}
	return prev[len(b)]
}

func min(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= a && b <= c {
		return b
	}
	return c
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
