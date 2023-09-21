package humanize

import (
	"fmt"
	"time"
)

func BytesToReadableSize(size int64) string {
	var unit string
	var converted float64

	if size >= 1<<30 {
		unit = "GB"
		converted = float64(size) / (1 << 30)
	} else if size >= 1<<20 {
		unit = "MB"
		converted = float64(size) / (1 << 20)
	} else if size >= 1<<10 {
		unit = "KB"
		converted = float64(size) / (1 << 10)
	} else {
		unit = "B"
		converted = float64(size)
	}

	return fmt.Sprintf("%.2f%s", converted, unit)
}

func TimeAgo(t time.Time) string {
	var duration time.Duration
	now := time.Now()
	if t.After(now) {
		duration = t.Sub(now)
	} else {
		duration = now.Sub(t)
	}

	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(duration.Seconds()))
	case duration < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(duration.Minutes()))
	case duration < time.Hour*24:
		return fmt.Sprintf("%d hours ago", int(duration.Hours()))
	case duration < time.Hour*24*7:
		return fmt.Sprintf("%d days ago", int(duration.Hours()/24))
	case duration < time.Hour*24*30:
		return fmt.Sprintf("%d weeks ago", int(duration.Hours()/(24*7)))
	case duration < time.Hour*24*365:
		return fmt.Sprintf("%d months ago", int(duration.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%d years ago", int(duration.Hours()/(24*365)))
	}
}
