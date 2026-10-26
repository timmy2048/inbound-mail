package retention

import "time"

// Cutoff is the instant a message must be strictly older than to be deleted.
// "Older than N days" means received before now minus N*24h. A message that
// is exactly N days old is kept.
func Cutoff(now time.Time, days int) time.Time {
	if days < 1 {
		days = 3
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

// Expired reports whether a message received at received should be deleted at now.
func Expired(received, now time.Time, days int) bool {
	return received.Before(Cutoff(now, days))
}
