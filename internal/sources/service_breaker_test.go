package sources_test

import (
	"fmt"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/sources"
)

// not_queried and not_configured say nothing about whether the source is
// healthy, so they neither count as a failure nor reset the failure count.
func TestServiceBreakerIgnoresNonAnswers(t *testing.T) {
	for _, quiet := range []string{evidence.SourceNotQueried, evidence.SourceNotConfigured} {
		t.Run(quiet, func(t *testing.T) {
			a := &fakeAdapter{name: evidence.SourceWikidata, outcomes: map[string]string{"quiet": quiet}}
			s := sources.NewService(&fakeFetcher{}, newMemCache(), a)
			look := func(isbn string) {
				s.Lookup(t.Context(), 1, []sources.Seed{{ISBN: isbn, Class: "file"}})
			}
			for i := range 4 {
				look(fmt.Sprintf("bad%d", i))
			}
			look("quiet") // must not reset the count of 4
			look("bad4")  // the 5th unavailable opens the breaker
			before := a.callCount()
			look("after")
			if a.callCount() != before {
				t.Fatalf("the breaker stayed closed after a %s answer reset its count", quiet)
			}
		})
	}
}
