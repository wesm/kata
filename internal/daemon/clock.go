package daemon

import (
	"fmt"
	"time"
)

// LocalDateBoundary returns the YYYY-MM-DD date of `at` interpreted in the
// IANA timezone `tz`. An empty tz defaults to UTC. Invalid timezones return
// an error.
//
// Used by view handlers to compute today's date boundary from the client's
// supplied X-Kata-Client-TZ header so "today" is correctly bucketed
// regardless of the server's wall clock zone.
func LocalDateBoundary(tz string, at time.Time) (string, error) {
	if tz == "" {
		return at.UTC().Format("2006-01-02"), nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	return at.In(loc).Format("2006-01-02"), nil
}
