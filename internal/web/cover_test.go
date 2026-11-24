package web

import (
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

func TestBookPageCover(t *testing.T) {
	f := &fakeReports{book: report.Book{Ref: library.BookRef{ID: 2, UUID: "u"}, State: verify.BookChecked, Package: &evidence.Package{
		Calibre: evidence.Record{Title: "T"},
		Cover: &evidence.CoverEvidence{State: evidence.StateOK, Bytes: 48213, SHA256: "ab12", Detected: "jpeg<b>", Width: 300, Height: 450,
			DHash: "0123456789abcdef", Flatness: 512},
	}}}
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 7, Status: verify.RunCompleted}})
	r := e.do(t, "GET", "/runs/7/books/2", nil, nil)
	for _, want := range []string{"<h2>Cover</h2>", "State: ok", "48213 bytes", "jpeg&lt;b&gt;", "300 × 450 px", "SHA-256: ab12", "Difference hash: 0123456789abcdef", "Flatness: 51.2"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("book page lacks %q", want)
		}
	}
	f.book.Package.Cover = &evidence.CoverEvidence{State: evidence.StateMissing, Reason: evidence.ReasonMissingOnDisk, Flatness: -1}
	r = e.do(t, "GET", "/runs/7/books/2", nil, nil)
	if !strings.Contains(r.body, "State: missing · missing_on_disk") || strings.Contains(r.body, "Flatness") {
		t.Error("a missing cover shows measures")
	}
	f.book.Package.Cover = nil
	if r = e.do(t, "GET", "/runs/7/books/2", nil, nil); !strings.Contains(r.body, "Not checked: this check predates cover checks.") {
		t.Error("a legacy package doesn't say covers weren't checked")
	}
}

func TestBookPageCoverCandidates(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	f := &fakeReports{book: report.Book{Ref: library.BookRef{ID: 2, UUID: "u"}, State: verify.BookChecked, Package: &evidence.Package{
		Calibre: evidence.Record{Title: "T"},
		Cover:   &evidence.CoverEvidence{State: evidence.StateMissing, Flatness: -1},
		Formats: []evidence.FormatEvidence{{Format: "EPUB", State: evidence.StateOK, FileCover: &evidence.CoverCandidate{
			Source: evidence.CandidateEPUB, State: evidence.StateOK, MediaType: "image/png", SHA256: sha, Width: 640, Height: 960, DHash: "0123456789abcdef", Flatness: 412}}},
	}}}
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.logIn(t)
	e.runs.add(verify.RunView{Run: verify.Run{ID: 7, Status: verify.RunCompleted}})
	r := e.do(t, "GET", "/runs/7/books/2", nil, nil)
	for _, want := range []string{"Cover candidate: the EPUB's own cover", "image/png", "640 × 960 px", "Difference hash: 0123456789abcdef · Flatness: 41.2"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("book page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "<img") {
		t.Error("the book page shows an image")
	}
}
