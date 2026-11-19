package machine

import (
	"time"
)

// Open reports whether t is inside a window. Hours without windows are always open.
func (h Hours) Open(t time.Time) bool {
	if len(h.Windows) == 0 {
		return true
	}
	l := t.In(h.loc)
	min := l.Hour()*60 + l.Minute()
	today, yesterday := l.Weekday(), (l.Weekday()+6)%7
	for _, w := range h.Windows {
		switch {
		case w.startMin < w.endMin:
			if w.days[today] && min >= w.startMin && min < w.endMin {
				return true
			}
		default:
			if (w.days[today] && min >= w.startMin) || (w.days[yesterday] && min < w.endMin) {
				return true
			}
		}
	}
	return false
}

// Next returns the earliest window start or end after t, or the zero time
// when there are no windows.
func (h Hours) Next(t time.Time) time.Time {
	var best time.Time
	consider := func(c time.Time) {
		if c.After(t) && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	l := t.In(h.loc)
	for _, w := range h.Windows {
		for off := -1; off <= 8; off++ {
			day := l.AddDate(0, 0, off)
			if !w.days[day.Weekday()] {
				continue
			}
			at := func(minutes, dayShift int) time.Time {
				return time.Date(day.Year(), day.Month(), day.Day()+dayShift, minutes/60, minutes%60, 0, 0, h.loc)
			}
			consider(at(w.startMin, 0))
			if w.startMin < w.endMin {
				consider(at(w.endMin, 0))
			} else {
				consider(at(w.endMin, 1))
			}
		}
	}
	return best
}
