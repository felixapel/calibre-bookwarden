package sources_test

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// TestHostsCoverAdapters: a production adapter whose host is missing from
// Hosts would be refused on every request.
func TestHostsCoverAdapters(t *testing.T) {
	var hosts []string
	for _, a := range []sources.Adapter{sources.DNB(), sources.BNE(), sources.OpenLibrary(), sources.GoogleBooks(""), sources.Wikidata(), sources.Hardcover("", nil)} {
		u, err := url.Parse(sources.Base(a))
		if err != nil || u.Scheme != "https" {
			t.Fatalf("%s base %q", a.Name(), sources.Base(a))
		}
		hosts = append(hosts, u.Host)
	}
	for host := range sources.HostPace {
		if !slices.Contains(sources.Hosts, host) {
			t.Fatalf("HostPace names %s, which is not in Hosts", host)
		}
	}
	if sources.HostPace["api.hardcover.app"] < 1100*time.Millisecond {
		t.Fatalf("Hardcover pace %v is under 1.1 s", sources.HostPace["api.hardcover.app"])
	}
	if !slices.Equal(hosts, sources.Hosts) {
		t.Fatalf("adapter hosts %v, Hosts %v", hosts, sources.Hosts)
	}
	if got := sources.UserAgent("1.2.3"); got != "bookwarden/1.2.3 (+https://github.com/felixapel/calibre-bookwarden)" {
		t.Fatalf("UserAgent = %q", got)
	}
}
