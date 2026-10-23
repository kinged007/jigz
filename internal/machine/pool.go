package machine

import "time"

// Available reports whether the entry can be used at t: not exhausted and
// inside its own hours.
func (e Entry) Available(st State, t time.Time) bool {
	return !t.Before(st[e.Name].ExhaustedUntil) && e.Hours.Open(t)
}

func (c *Config) usable(e Entry, st State, t time.Time) bool {
	return !c.Unsupported[e.Runtime] && e.Available(st, t)
}

// Pick returns the first available entry of the runtime in config order.
func (c *Config) Pick(st State, runtime string, t time.Time) (Entry, bool) {
	for _, e := range c.Pool {
		if e.Runtime == runtime && c.usable(e, st, t) {
			return e, true
		}
	}
	return Entry{}, false
}

// ActiveRuntime returns the runtime of the first available entry, or "" when
// none is available.
func (c *Config) ActiveRuntime(st State, t time.Time) string {
	for _, e := range c.Pool {
		if c.usable(e, st, t) {
			return e.Runtime
		}
	}
	return ""
}

// NextChange returns the earliest time after t at which the machine hours or
// an entry's availability changes, or the zero time when nothing is scheduled.
func (c *Config) NextChange(st State, t time.Time) time.Time {
	var best time.Time
	consider := func(c time.Time) {
		if c.After(t) && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	consider(c.Hours.Next(t))
	for _, e := range c.Pool {
		consider(e.Hours.Next(t))
		consider(st[e.Name].ExhaustedUntil)
	}
	return best
}
