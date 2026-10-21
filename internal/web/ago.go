package web

import (
	"fmt"
	"time"
)

// ago says how long before now t was, the way a feed does: "just now", "5 mins ago", "3 hrs ago",
// "2 days ago", "3 wks ago", "4 mos ago", "2 yrs ago". The exact time is kept in the tooltip.
func ago(t time.Time) string { return agoAt(time.Now(), t) }

func agoAt(now, t time.Time) string {
	d := now.Sub(t)
	unit := func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s ago", one)
		}
		return fmt.Sprintf("%d %s ago", n, many)
	}
	switch {
	case d < time.Minute:
		return "just now" // also future times, from clocks that differ a little
	case d < time.Hour:
		return unit(int(d/time.Minute), "min", "mins")
	case d < 24*time.Hour:
		return unit(int(d/time.Hour), "hr", "hrs")
	case d < 7*24*time.Hour:
		return unit(int(d/(24*time.Hour)), "day", "days")
	case d < 30*24*time.Hour:
		return unit(int(d/(7*24*time.Hour)), "wk", "wks")
	case d < 365*24*time.Hour:
		return unit(int(d/(30*24*time.Hour)), "mo", "mos")
	}
	return unit(int(d/(365*24*time.Hour)), "yr", "yrs")
}
