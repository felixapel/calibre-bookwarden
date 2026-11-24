package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

type stageFailure struct {
	*os.File
	fail string
}

func (f stageFailure) Write(p []byte) (int, error) {
	if f.fail == "write" {
		return 0, io.ErrClosedPipe
	}
	return f.File.Write(p)
}
func (f stageFailure) WriteString(s string) (int, error) { return f.Write([]byte(s)) }
func (f stageFailure) Close() error {
	err := f.File.Close()
	if f.fail == "close" {
		return io.ErrClosedPipe
	}
	if f.fail == "reopen" {
		return os.Remove(f.Name())
	}
	return err
}

func TestCSVStaging(t *testing.T) {
	for _, name := range []string{"success", "late_integrity", "cancel", "deadline", "oversize", "write", "close", "reopen"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)
			f := &fakeReports{}
			f.csv = func(w io.Writer) error {
				dirs, err := filepath.Glob(filepath.Join(root, "bookwarden-export-*"))
				if err != nil || len(dirs) != 1 {
					t.Fatalf("staging dirs=%v/%v", dirs, err)
				}
				info, err := os.Stat(dirs[0])
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("directory mode=%v/%v", info, err)
				}
				files, err := os.ReadDir(dirs[0])
				if err != nil || len(files) != 1 {
					t.Fatalf("staging files=%v/%v", files, err)
				}
				info, err = files[0].Info()
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("file mode=%v/%v", info, err)
				}
				if _, err := io.WriteString(w, "private-row\n"); err != nil {
					return err
				}
				switch name {
				case "late_integrity":
					return report.ErrIntegrity
				case "cancel":
					return context.Canceled
				case "deadline":
					return context.DeadlineExceeded
				case "oversize":
					return report.ErrExportTooLarge
				}
				return nil
			}
			e := newEnv(t, func(o *Options) { o.Reports = f })
			e.setUpAdmin(t)
			e.logIn(t)
			if name == "write" || name == "close" || name == "reopen" {
				e.server.createExportFile = func(path string) (io.WriteCloser, error) {
					file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
					if err != nil {
						return nil, err
					}
					return stageFailure{file, name}, nil
				}
			}
			r := e.do(t, "GET", "/runs/7/findings.csv?code=title_mismatch", nil, nil)
			want := 500
			switch name {
			case "success":
				want = 200
			case "cancel":
				want = 408
			case "deadline":
				want = 504
			case "oversize":
				want = 422
			}
			if r.status != want {
				t.Fatalf("%s status=%d body=%s", name, r.status, r.body)
			}
			if want != 200 && (r.header.Get("Content-Disposition") != "" || strings.Contains(r.body, "private-row")) {
				t.Fatal("failed export started attachment")
			}
			if want == 200 && (r.header.Get("Content-Length") != "12" || !strings.Contains(r.header.Get("Content-Disposition"), "bookwarden-run-7-findings.csv") || r.header.Get("Cache-Control") != "no-store") {
				t.Fatalf("attachment headers=%v", r.header)
			}
			dirs, _ := filepath.Glob(filepath.Join(root, "bookwarden-export-*"))
			if len(dirs) != 0 {
				t.Fatalf("leaked dirs=%v", dirs)
			}
		})
	}
}
func TestCSVBusyAndSelection(t *testing.T) {
	e := signedIn(t)
	if e.server.exportSlots == nil {
		t.Fatal("missing export slot")
	}
	e.server.exportSlots <- struct{}{}
	if r := e.do(t, "GET", "/runs/7/findings.csv", nil, nil); r.status != 503 || r.header.Get("Retry-After") != "1" {
		t.Fatalf("busy=%d/%v", r.status, r.header)
	}
	<-e.server.exportSlots
	for _, path := range []string{"/runs/7/findings.csv?cursor=anything", "/runs/7/findings.csv?code=bad", "/runs/7/findings.csv?format=pdf", "/runs/7/findings.csv?x=1"} {
		if r := e.do(t, "GET", path, nil, nil); r.status != 400 {
			t.Fatalf("%s=%d", path, r.status)
		}
	}
}
func TestRecheckIdentity(t *testing.T) {
	f := &fakeReports{book: report.Book{Ref: library.BookRef{ID: 2, UUID: "historical"}}}
	e := newEnv(t, func(o *Options) { o.Reports = f })
	e.setUpAdmin(t)
	e.runs.add(running(7))
	if r := e.do(t, "POST", "/runs/7/books/2/recheck", url.Values{}, nil); r.status != 401 {
		t.Fatalf("login=%d", r.status)
	}
	e.logIn(t)
	token := e.csrfToken(t)
	if r := e.do(t, "POST", "/runs/7/books/2/recheck", url.Values{}, nil); r.status != 403 {
		t.Fatalf("CSRF=%d", r.status)
	}
	if r := e.do(t, "POST", "/runs/7/books/2/recheck", url.Values{"csrf_token": {token}}, http.Header{"Origin": {"http://evil.example"}}); r.status != 403 {
		t.Fatalf("Origin=%d", r.status)
	}
	for _, state := range []verify.BookState{verify.BookChecked, verify.BookFailed, verify.BookSourceChanged, verify.BookPending} {
		f.book.State = state
		r := e.do(t, "POST", "/runs/7/books/2/recheck", url.Values{"csrf_token": {token}, "uuid": {"attacker"}, "library_id": {"attacker"}}, nil)
		if r.status != 303 || r.header.Get("Location") != "/runs/7" {
			t.Fatalf("%s=%d/%s", state, r.status, r.body)
		}
		got := e.runs.started[len(e.runs.started)-1]
		if got != (verify.Scope{BookID: 2, ExpectedLibraryID: "library", ExpectedUUID: "historical"}) {
			t.Fatalf("scope=%+v", got)
		}
	}
	for _, tt := range []struct {
		err    error
		status int
	}{{verify.ErrBookChanged, 409}, {verify.ErrRunActive, 409}, {library.ErrBookNotFound, 404}, {verify.ErrInvalidScope, 400}} {
		e.runs.startErr = tt.err
		if r := e.do(t, "POST", "/runs/7/books/2/recheck", url.Values{"csrf_token": {token}}, nil); r.status != tt.status {
			t.Fatalf("%v=%d", tt.err, r.status)
		}
	}
	e.runs.startErr = nil
	for _, tt := range []struct {
		err    error
		status int
	}{{report.ErrIntegrity, 500}, {report.ErrNoSuchBook, 404}} {
		f.err = tt.err
		n := len(e.runs.started)
		r := e.do(t, "POST", "/runs/7/books/2/recheck", url.Values{"csrf_token": {token}}, nil)
		if r.status != tt.status || len(e.runs.started) != n {
			t.Fatalf("historical guard=%d", r.status)
		}
	}
}
