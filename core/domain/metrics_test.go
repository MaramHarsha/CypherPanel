package domain

import "testing"

// The claim the whole write budget rests on: percentiles do not average, but
// histograms add — so a window's percentile is computed once from one summed
// histogram and is accurate to the bucket boundary.
//
// This test is the arithmetic proof. Two buckets whose own p95s are 100 ms and
// 2000 ms have an hour-p95 that is NEITHER the mean nor the max of them, and no
// operation on those two numbers recovers it.
func TestPercentilesDoNotAverageButHistogramsAdd(t *testing.T) {
	// Bucket A: 1000 requests, nearly all fast. p95 lands at 100 ms.
	a := make([]int64, LatencySlots)
	a[3] = 940 // ≤ 10 ms
	a[6] = 60  // ≤ 100 ms
	// Bucket B: 20 requests, nearly all slow. p95 lands at 2500 ms.
	b := make([]int64, LatencySlots)
	b[10] = 19 // ≤ 2500 ms
	b[3] = 1

	pa, pb := Percentile(a, 0.95), Percentile(b, 0.95)
	if pa != 100 || pb != 2500 {
		t.Fatalf("fixture p95s are %v and %v; the test needs 100 and 2500", pa, pb)
	}

	merged := MergeHistograms(append([]int64(nil), a...), b)
	got := Percentile(merged, 0.95)

	// The true combined p95 is 100 ms: the twenty slow requests are under 5% of
	// 1020. Both the mean (1300) and the max (2500) of the two bucket p95s are
	// wrong, and this is exactly why nothing stores a percentile.
	if got != 100 {
		t.Fatalf("combined p95 = %v, want 100", got)
	}
	mean := (pa + pb) / 2
	if got == mean || got == pb {
		t.Fatalf("the combined p95 (%v) coincides with the mean (%v) or max (%v) of the bucket p95s — the fixture no longer proves anything", got, mean, pb)
	}
}

// Means are derived from accumulators over the covered time, never stored. A
// bucket that covered 120 seconds of its 300 must not read as a dip.
func TestAMeanOverUnequalCoverageIsNotAnAverageOfAverages(t *testing.T) {
	// One bucket fully covered at 10% CPU, one covering 60s at 100%.
	full := ResourceMetricBucket{CPUCoreMs: 30_000, CoveredSeconds: 300}   // 10%
	partial := ResourceMetricBucket{CPUCoreMs: 60_000, CoveredSeconds: 60} // 100%

	totalMs := full.CPUCoreMs + partial.CPUCoreMs
	totalCovered := full.CoveredSeconds + partial.CoveredSeconds
	correct := float64(totalMs) / float64(totalCovered*1000) * 100

	naive := (10.0 + 100.0) / 2
	if correct == naive {
		t.Fatal("the fixture no longer distinguishes the two computations")
	}
	if correct < 24 || correct > 26 {
		t.Fatalf("correct mean = %v%%, want ~25%% — 90 core-seconds over 360 covered", correct)
	}
}

func TestPercentileOfAnEmptyHistogramIsZeroNotAGuess(t *testing.T) {
	if got := Percentile(make([]int64, LatencySlots), 0.95); got != 0 {
		t.Errorf("got %v, want 0 — no data must not produce a number", got)
	}
}

// The overflow slot answers with the last finite bound, because "at least 60
// seconds" is what the data supports.
func TestTheOverflowSlotDoesNotInventANumber(t *testing.T) {
	h := make([]int64, LatencySlots)
	h[LatencySlots-1] = 10
	if got := Percentile(h, 0.95); got != 60000 {
		t.Errorf("got %v, want 60000", got)
	}
}

// A resource with no buckets must be unknown, never zero: a flat line at 0%
// reads as an idle application, and an old agent reports nothing (ADR-010).
func TestDefaultSettingsLeaveRequestAnalyticsOff(t *testing.T) {
	d := DefaultMetricsSettings()
	if !d.Enabled {
		t.Error("collection should be on by default")
	}
	if d.RequestAnalytics {
		t.Error("request analytics must be OFF by default: paths are application-authored data, and an operator for whom URLs are sensitive must not have that decision made for them")
	}
	if 3600%d.BucketSeconds != 0 {
		t.Errorf("the default bucket (%ds) does not divide an hour", d.BucketSeconds)
	}
}
