package sources

import "time"

// SetBase points an adapter at a test server or fake fetcher.
func SetBase(a Adapter, base string) {
	switch a := a.(type) {
	case *sru:
		a.base = base
	case *openLibrary:
		a.base = base
	case *googleBooks:
		a.base = base
	case *wikidata:
		a.base = base
	case *hardcover:
		a.base = base
	}
}

// SetClock replaces the clock a Hardcover adapter counts its days by.
func SetClock(a Adapter, now func() time.Time) {
	if h, ok := a.(*hardcover); ok {
		h.now = now
	}
}

// SetNationalPrefixes replaces the root-sharing table for one test.
func SetNationalPrefixes(m map[string][]string) func() {
	old := nationalMARCPrefixes
	nationalMARCPrefixes = m
	return func() { nationalMARCPrefixes = old }
}

// Base returns an adapter's base URL.
func Base(a Adapter) string {
	switch a := a.(type) {
	case *sru:
		return a.base
	case *openLibrary:
		return a.base
	case *googleBooks:
		return a.base
	case *wikidata:
		return a.base
	case *hardcover:
		return a.base
	}
	return ""
}

// NationalPrefixes returns the root-sharing table.
func NationalPrefixes() map[string][]string { return nationalMARCPrefixes }

// SetRand replaces the random source of the cache TTL jitter.
func SetRand(s *Service, f func() float64) { s.rand = f }

// HostNext returns when c may next ask host (host:port).
func HostNext(c *Client, host string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.next[host]
}

// HoldHost holds host (host:port) for d from now.
func HoldHost(c *Client, host string, d time.Duration) { c.hold(host, d) }
