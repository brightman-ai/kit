package agentanalytics

import (
	"math"
	"sort"
	"time"
)

type Money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type PerformanceObservation struct {
	WorkItemID         string
	Runtime            string
	Profile            TaskProfile
	Outcome            OutcomeStatus
	FirstPass          *bool
	SubmittedAt        time.Time
	AcceptedAt         *time.Time
	Cost               *Money
	AvoidableAttention int
	Models             []string
}

type RateEstimate struct {
	Value *float64 `json:"value,omitempty"`
	N     int      `json:"n"`
	Low   *float64 `json:"low,omitempty"`
	High  *float64 `json:"high,omitempty"`
}

type CohortProfile struct {
	CohortKey            string             `json:"cohort_key"`
	Runtime              string             `json:"runtime"`
	EligibleN            int                `json:"eligible_n"`
	ObservedN            int                `json:"observed_n"`
	VCR                  RateEstimate       `json:"verified_completion_rate"`
	FPR                  RateEstimate       `json:"first_pass_rate"`
	TTAVMedian           *time.Duration     `json:"ttav_median,omitempty"`
	TTAVP75              *time.Duration     `json:"ttav_p75,omitempty"`
	CostPerAccepted      map[string]float64 `json:"cost_per_accepted"`
	AttentionPerAccepted *float64           `json:"attention_per_accepted,omitempty"`
}

func BuildCohortProfiles(observations []PerformanceObservation) []CohortProfile {
	type key struct{ cohort, runtime string }
	groups := make(map[key][]PerformanceObservation)
	for _, observation := range observations {
		if !observation.Profile.Comparable() || observation.Runtime == "" {
			continue
		}
		k := key{observation.Profile.CohortKey(), observation.Runtime}
		groups[k] = append(groups[k], observation)
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].cohort == keys[j].cohort {
			return keys[i].runtime < keys[j].runtime
		}
		return keys[i].cohort < keys[j].cohort
	})
	out := make([]CohortProfile, 0, len(keys))
	for _, k := range keys {
		out = append(out, buildCohortProfile(k.cohort, k.runtime, groups[k]))
	}
	return out
}

func buildCohortProfile(cohort, runtime string, observations []PerformanceObservation) CohortProfile {
	p := CohortProfile{CohortKey: cohort, Runtime: runtime, ObservedN: len(observations), CostPerAccepted: make(map[string]float64)}
	var success, firstPassSuccess, firstPassN, accepted, attention int
	var ttav []time.Duration
	costs := make(map[string]float64)
	for _, observation := range observations {
		eligible := observation.Outcome == OutcomeVerifiedPass || observation.Outcome == OutcomeVerifiedFail ||
			observation.Outcome == OutcomeHumanAccepted || observation.Outcome == OutcomeHumanRework
		if !eligible {
			continue
		}
		p.EligibleN++
		passed := observation.Outcome == OutcomeVerifiedPass || observation.Outcome == OutcomeHumanAccepted
		if passed {
			success++
			accepted++
			attention += observation.AvoidableAttention
			if observation.AcceptedAt != nil && !observation.SubmittedAt.IsZero() && !observation.AcceptedAt.Before(observation.SubmittedAt) {
				ttav = append(ttav, observation.AcceptedAt.Sub(observation.SubmittedAt))
			}
			if observation.Cost != nil && observation.Cost.Currency != "" {
				costs[observation.Cost.Currency] += observation.Cost.Amount
			}
		}
		if observation.FirstPass != nil {
			firstPassN++
			if *observation.FirstPass {
				firstPassSuccess++
			}
		}
	}
	p.VCR = estimateRate(success, p.EligibleN)
	p.FPR = estimateRate(firstPassSuccess, firstPassN)
	if len(ttav) > 0 {
		sort.Slice(ttav, func(i, j int) bool { return ttav[i] < ttav[j] })
		median, p75 := percentileDuration(ttav, .5), percentileDuration(ttav, .75)
		p.TTAVMedian, p.TTAVP75 = &median, &p75
	}
	if accepted > 0 {
		for currency, total := range costs {
			p.CostPerAccepted[currency] = total / float64(accepted)
		}
		value := float64(attention) / float64(accepted)
		p.AttentionPerAccepted = &value
	}
	return p
}

type ComparisonPolicy struct {
	Version            string  `json:"version"`
	MinimumEligibleN   int     `json:"minimum_eligible_n"`
	QualityMargin      float64 `json:"quality_margin"`
	MinimumImprovement float64 `json:"minimum_improvement"`
}

var DefaultComparisonPolicy = ComparisonPolicy{
	Version: "noninferiority-v1", MinimumEligibleN: 5, QualityMargin: .05, MinimumImprovement: .05,
}

type ComparisonDecision struct {
	CohortKey      string   `json:"cohort_key"`
	Candidate      string   `json:"candidate"`
	Baseline       string   `json:"baseline"`
	Status         string   `json:"status"`
	Reason         string   `json:"reason"`
	QualityDiff    *float64 `json:"quality_diff,omitempty"`
	QualityDiffLow *float64 `json:"quality_diff_low,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
	PolicyVersion  string   `json:"policy_version"`
}

func CompareCohorts(candidate, baseline CohortProfile, policy ComparisonPolicy) ComparisonDecision {
	d := ComparisonDecision{CohortKey: candidate.CohortKey, Candidate: candidate.Runtime, Baseline: baseline.Runtime, PolicyVersion: policy.Version}
	if candidate.CohortKey == "" || candidate.CohortKey != baseline.CohortKey {
		d.Status, d.Reason = "not_comparable", "task_profile_differs"
		return d
	}
	if candidate.EligibleN < policy.MinimumEligibleN || baseline.EligibleN < policy.MinimumEligibleN || candidate.VCR.Value == nil || baseline.VCR.Value == nil {
		d.Status, d.Reason = "insufficient_evidence", "eligible_sample_below_minimum"
		return d
	}
	diff := *candidate.VCR.Value - *baseline.VCR.Value
	se := math.Sqrt((*candidate.VCR.Value*(1-*candidate.VCR.Value))/float64(candidate.EligibleN) +
		(*baseline.VCR.Value*(1-*baseline.VCR.Value))/float64(baseline.EligibleN))
	low := diff - 1.96*se
	d.QualityDiff, d.QualityDiffLow = &diff, &low
	if low < -policy.QualityMargin {
		d.Status, d.Reason = "quality_lower", "quality_noninferiority_not_met"
		return d
	}
	improvements := make([]string, 0, 3)
	if candidate.TTAVMedian != nil && baseline.TTAVMedian != nil && *baseline.TTAVMedian > 0 &&
		float64(*baseline.TTAVMedian-*candidate.TTAVMedian)/float64(*baseline.TTAVMedian) >= policy.MinimumImprovement {
		improvements = append(improvements, "faster")
	}
	for currency, baselineCost := range baseline.CostPerAccepted {
		candidateCost, ok := candidate.CostPerAccepted[currency]
		if ok && baselineCost > 0 && (baselineCost-candidateCost)/baselineCost >= policy.MinimumImprovement {
			improvements = append(improvements, "cheaper_"+currency)
		}
	}
	if candidate.AttentionPerAccepted != nil && baseline.AttentionPerAccepted != nil && *baseline.AttentionPerAccepted > 0 &&
		(*baseline.AttentionPerAccepted-*candidate.AttentionPerAccepted)/(*baseline.AttentionPerAccepted) >= policy.MinimumImprovement {
		improvements = append(improvements, "less_attention")
	}
	if len(improvements) == 0 {
		d.Status, d.Reason = "pareto_tradeoff", "quality_noninferior_without_material_improvement"
		return d
	}
	d.Status, d.Reason = "recommended", "quality_noninferior_with_material_improvement"
	d.Recommendation = stringsJoin(improvements, ",")
	return d
}

// ModelCapabilityEligible prevents a mixed-model orchestration outcome from
// being credited to every model that happened to participate.
func ModelCapabilityEligible(models []string) (string, bool) {
	seen := make(map[string]struct{})
	for _, model := range models {
		if model != "" {
			seen[model] = struct{}{}
		}
	}
	if len(seen) != 1 {
		return "", false
	}
	for model := range seen {
		return model, true
	}
	return "", false
}

func estimateRate(success, n int) RateEstimate {
	r := RateEstimate{N: n}
	if n == 0 {
		return r
	}
	p := float64(success) / float64(n)
	z := 1.96
	denom := 1 + z*z/float64(n)
	center := (p + z*z/(2*float64(n))) / denom
	half := z * math.Sqrt((p*(1-p)+z*z/(4*float64(n)))/float64(n)) / denom
	low, high := math.Max(0, center-half), math.Min(1, center+half)
	r.Value, r.Low, r.High = &p, &low, &high
	return r
}

func percentileDuration(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	idx := int(math.Ceil(quantile*float64(len(values)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func stringsJoin(values []string, separator string) string {
	if len(values) == 0 {
		return ""
	}
	out := values[0]
	for _, value := range values[1:] {
		out += separator + value
	}
	return out
}
