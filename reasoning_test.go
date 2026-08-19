package gage

import "testing"

func TestReasoningEffortCanonical(t *testing.T) {
	cases := []struct {
		in    ReasoningEffort
		want  ReasoningEffort
		known bool
	}{
		{"", ReasoningNone, true},
		{"none", ReasoningOff, true},
		{"OFF", ReasoningOff, true},
		{"disabled", ReasoningOff, true},
		{"minimal", ReasoningMinimal, true},
		{"very low", ReasoningMinimal, true},
		{"low", ReasoningLow, true},
		{"Medium", ReasoningMedium, true},
		{"med", ReasoningMedium, true},
		{"high", ReasoningHigh, true},
		{"x-high", ReasoningXHigh, true},
		{"extra_high", ReasoningXHigh, true},
		{"max", ReasoningMax, true},
		{"ultra", ReasoningMax, true},
		// A gateway's own label: unknown, left to the provider to pass through
		// verbatim or reject.
		{"turbo", "", false},
		{"thinking-64k", "", false},
	}
	for _, tc := range cases {
		got, ok := tc.in.Canonical()
		if ok != tc.known || got != tc.want {
			t.Errorf("%q.Canonical() = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.known)
		}
	}
}
