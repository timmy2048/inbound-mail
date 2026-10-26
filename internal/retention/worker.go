package retention

import (
	"context"
	"log"
	"time"

	"laijian/internal/store"
)

// Start deletes expired messages on startup and then every 15 minutes.
func Start(ctx context.Context, st *store.Store, days int) {
	const every = 15 * time.Minute
	go func() {
		run(st, days)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run(st, days)
			}
		}
	}()
}

func run(st *store.Store, days int) {
	now := time.Now()
	n, err := st.DeleteReceivedBefore(Cutoff(now, days))
	if err != nil {
		log.Printf("retention: %v", err)
	} else if n > 0 {
		log.Printf("retention: deleted %d messages older than %d days", n, days)
	}
	if err := st.PruneSessions(now); err != nil {
		log.Printf("retention: sessions: %v", err)
	}
}
