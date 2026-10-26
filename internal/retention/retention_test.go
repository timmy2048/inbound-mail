package retention

import (
	"testing"
	"time"
)

func TestRetentionFilter(t *testing.T) {
	now := time.Date(2026, 10, 26, 5, 0, 0, 0, time.UTC)
	cutoff := Cutoff(now, 3)
	want := now.Add(-72 * time.Hour)
	if !cutoff.Equal(want) {
		t.Fatalf("cutoff = %s, want %s", cutoff, want)
	}

	cases := []struct {
		name     string
		received time.Time
		expired  bool
	}{
		{"just older than 3 days", cutoff.Add(-time.Millisecond), true},
		{"exactly 3 days", cutoff, false},
		{"newer than 3 days", cutoff.Add(time.Second), false},
		{"four days", now.Add(-96 * time.Hour), true},
		{"one day", now.Add(-24 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Expired(tc.received, now, 3); got != tc.expired {
				t.Fatalf("Expired(%s) = %v, want %v", tc.received, got, tc.expired)
			}
		})
	}

	if !Cutoff(now, 0).Equal(Cutoff(now, 3)) {
		t.Fatal("non-positive days should fall back to 3")
	}
}
