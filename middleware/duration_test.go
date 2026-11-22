// Package middleware holds the deterministic unit tests for the T-2 duration
// conversion. They pin the fractional-millisecond contract without depending on
// wall-clock timing.
package middleware

import (
	"testing"
	"time"
)

// TestDurationMillisKeepsSubMillisecondPrecision is the regression test for the
// review finding that whole-millisecond durations logged 0 for fast handlers.
// durationMillis must report fractional milliseconds, rounded to the nearest
// microsecond, so a sub-millisecond handler stays measurable.
func TestDurationMillisKeepsSubMillisecondPrecision(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want float64
	}{
		{name: "zero stays zero", d: 0, want: 0},
		{name: "sub-microsecond rounds to zero", d: 400 * time.Nanosecond, want: 0},
		{name: "half a microsecond rounds up", d: 500 * time.Nanosecond, want: 0.001},
		{name: "one microsecond", d: time.Microsecond, want: 0.001},
		{name: "sub-millisecond handler is not truncated to zero", d: 412 * time.Microsecond, want: 0.412},
		{name: "just under a millisecond", d: 999 * time.Microsecond, want: 0.999},
		{name: "fraction above one millisecond is preserved", d: 1500 * time.Microsecond, want: 1.5},
		{name: "fraction is preserved on long durations", d: 1234567 * time.Microsecond, want: 1234.567},
		{name: "twenty milliseconds", d: 20 * time.Millisecond, want: 20},
		{name: "one second", d: time.Second, want: 1000},
		{name: "negative durations clamp to zero", d: -time.Millisecond, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := durationMillis(tc.d); got != tc.want {
				t.Errorf("durationMillis(%v) = %v, want %v", tc.d, got, tc.want)
			}
		})
	}
}
