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

// stamp and stampAt show a time the way every page does, in the server's time zone. A time read from a feed
// is in the zone the feed wrote it in (often UTC), one read from the database is in the server's: without
// this the same reply is 16:10 in one view of a thread and 20:10 in another.
func stamp(t time.Time) string   { return t.Local().Format("2 Jan 2006 15:04") }
func stampAt(t time.Time) string { return t.Local().Format("2 Jan 2006 at 15:04") }
