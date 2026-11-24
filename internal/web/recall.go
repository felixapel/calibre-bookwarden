package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/eval"
	"github.com/felixapel/calibre-bookwarden/internal/evidence"
	"github.com/felixapel/calibre-bookwarden/internal/report"
	"github.com/felixapel/calibre-bookwarden/internal/store"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
)

// Recall is the recall sample (design section 6.4) as the pages need it.
// *eval.RecallService implements it. The answers it records are
// confirmations, never labels.
type Recall interface {
	Start(ctx context.Context, seed *int64, by string) (int64, error)
	Latest(ctx context.Context) (store.RecallSample, error)
	Sample(ctx context.Context, id int64) (store.RecallSample, error)
	BookAt(ctx context.Context, id int64, position int) (store.SampleBook, error)
	Answers(ctx context.Context, id int64, position int) (map[string]store.Confirmation, error)
	Confirm(ctx context.Context, id int64, position int, answers []store.Confirmation, by string) error
	// Status is the sample's progress. With reveal, a complete sample is
	// closed and its results returned; otherwise they stay nil until it is.
	Status(ctx context.Context, id int64, reveal bool) (eval.SampleStatus, error)
}

var _ Recall = (*eval.RecallService)(nil)

// SampleView is the /sample page: the current sample's progress and, once
// something is answered, its recall.
type SampleView struct {
	ID        int64
	Seed      int64
	Size      int
	CreatedAt time.Time
	CreatedBy string
	Answered  int
	// Results is nil until every book has an answer: while the sample is
	// open the page shows progress only, because the recall is Bookwarden's
	// opinion of the books being answered blind.
	Results *SampleResults
	// Next is the position of the first book with no answer, 0 when every
	// book has one.
	Next int
}

// SampleResults is the recall of a complete, closed sample.
type SampleResults struct {
	Rows            []RecallRow
	FalseAlarms     []string
	Unrepresented   int
	Library         int
	FindingsVersion int
	DecisionVersion int
	ClosedAt        time.Time
}

// RecallRow is one field of the recall table.
type RecallRow struct {
	Field                string
	Wrong, Found, Right  int
	Skipped, FalseAlarms int
	WrongValue           int
	EffectiveN           string
	Recall, Interval     string
}

// fieldLabels name the fields as the sample pages do.
var fieldLabels = map[string]string{
	"title": "Title", "authors": "Authors", "publisher": "Publisher", "pubdate": "Published", "isbn": "ISBN", "languages": "Languages",
}

// SampleBookView is one blind page. It holds Calibre's values and the
// book's front-matter text and nothing else: no proposal, finding, tier,
// reason or root of the book is ever put in it.
type SampleBookView struct {
	SampleID  int64
	Position  int
	Size      int
	Fields    []SampleField
	Excerpts  []SampleExcerpt
	Closed    bool
	PrevURL   string
	NextURL   string
	AnswerURL string
}

// SampleField is one question: Calibre's value and the answer given so far.
type SampleField struct {
	Name, Label, Calibre string
	Answer, Typed        string
	Hint                 string
	Key                  int // the access key of "Calibre right"
}

// SampleExcerpt is one format's front-matter text, plain.
type SampleExcerpt struct {
	Format    string
	Chars     int
	Truncated bool
	Complete  bool
	Available bool
	Text      string
}

func (s *Server) recallOff(w http.ResponseWriter) bool {
	if s.opts.Recall == nil {
		http.Error(w, "Not found.", http.StatusNotFound)
		return true
	}
	return false
}

// recallError answers a failed sample request.
func (s *Server) recallError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNoSuchSample):
		http.Error(w, "Sample or book not found.", http.StatusNotFound)
	case errors.Is(err, store.ErrSampleClosed):
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Recall sample", Error: "This sample is closed: its results were shown, so its answers are final. Draw a new sample to answer again.", BackURL: "/sample", BackText: "Back to the sample"})
	case errors.Is(err, verify.ErrNoSuchRun), errors.Is(err, report.ErrNoSuchBook):
		s.refuse(w, r, http.StatusGone, pageData{Title: "Recall sample", Error: "This run’s evidence was pruned, so the book can’t be shown. Its answers, if any, are kept.", BackURL: "/sample", BackText: "Back to the sample"})
	case errors.Is(err, eval.ErrInvalidAnswer):
		http.Error(w, "Invalid answer.", http.StatusBadRequest)
	default:
		s.reportError(w, r, err)
	}
}

// samplePage shows the current sample's progress, or the form that starts one.
func (s *Server) samplePage(w http.ResponseWriter, r *http.Request) {
	if s.recallOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	data := pageData{Title: "Recall sample", SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken}
	sample, err := s.opts.Recall.Latest(ctx)
	switch {
	case errors.Is(err, store.ErrNoSuchSample):
		s.render(w, 200, "sample.html", data)
		return
	case err != nil:
		s.recallError(w, r, err)
		return
	}
	// Showing the results of a complete sample closes it (once, recorded).
	st, err := s.opts.Recall.Status(ctx, sample.ID, true)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.recallError(w, r, err)
		return
	}
	data.Sample = sampleView(st)
	s.render(w, 200, "sample.html", data)
}

func sampleView(st eval.SampleStatus) *SampleView {
	sample := st.Sample
	v := &SampleView{ID: sample.ID, Seed: sample.Seed, Size: sample.Size, CreatedAt: sample.CreatedAt, CreatedBy: sample.CreatedBy,
		Answered: st.Answered, Next: st.Next}
	if st.Result == nil {
		return v
	}
	res := st.Result
	v.Results = &SampleResults{Unrepresented: res.Unrepresented, Library: res.Library, FindingsVersion: res.FindingsVersion, DecisionVersion: res.DecisionVersion}
	if sample.ClosedAt != nil {
		v.Results.ClosedAt = *sample.ClosedAt
	}
	for _, f := range res.Fields {
		row := RecallRow{Field: fieldLabels[f.Field], Wrong: f.Wrong, Found: f.Found, Right: f.Right, Skipped: f.Skipped,
			FalseAlarms: len(f.FalseAlarms), WrongValue: f.WrongValue, EffectiveN: "—", Recall: f.RecallText(), Interval: f.Interval()}
		if f.Wrong > 0 {
			row.EffectiveN = fmt.Sprintf("%.1f", f.EffectiveN)
		}
		v.Results.Rows = append(v.Results.Rows, row)
		if len(f.FalseAlarms) > 0 {
			ids := make([]string, len(f.FalseAlarms))
			for i, id := range f.FalseAlarms {
				ids[i] = strconv.Itoa(id)
			}
			v.Results.FalseAlarms = append(v.Results.FalseAlarms, fieldLabels[f.Field]+": books "+strings.Join(ids, ", "))
		}
	}
	return v
}

// startSample draws a sample. The seed is optional; a random one is recorded
// when it is empty.
func (s *Server) startSample(w http.ResponseWriter, r *http.Request) {
	if s.recallOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), decideBudget)
	defer cancel()
	form, err := strictForm(r, "", "seed")
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	var seed *int64
	if raw := strings.TrimSpace(form.Get("seed")); raw != "" {
		n, err := positiveSeed(raw)
		if err != nil {
			s.reportError(w, r, report.ErrInvalidFilter)
			return
		}
		seed = &n
	}
	id, err := s.opts.Recall.Start(ctx, seed, actor)
	switch {
	case err == nil:
		http.Redirect(w, r, fmt.Sprintf("/sample/%d/book/1", id), http.StatusSeeOther)
	case errors.Is(err, store.ErrNoCheckedRun):
		s.refuse(w, r, http.StatusConflict, pageData{Title: "Recall sample", Error: "Waiting for the nightly seal check to re-verify the latest run at the current rules (up to 25 h after an upgrade). A sample is drawn only from a run whose seal check passed at this version; if there is no whole-library run yet, finish one first.", BackURL: "/sample", BackText: "Back to the sample"})
	default:
		s.recallError(w, r, err)
	}
}

// positiveSeed reads a seed: digits only, 0 to 2^63-1.
func positiveSeed(raw string) (int64, error) {
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, report.ErrInvalidFilter
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, report.ErrInvalidFilter
	}
	return n, nil
}

// sampleBookPage shows one sampled book, blind. It reads the book through the
// report service, which opens its sealed package and verifies the seal, and
// shows only Calibre's values and the excerpt.
func (s *Server) sampleBookPage(w http.ResponseWriter, r *http.Request) {
	if s.recallOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	if _, err := strictQuery(r); err != nil {
		s.reportError(w, r, err)
		return
	}
	sample, pos, err := s.sampleAndPosition(ctx, r)
	if err != nil {
		s.recallError(w, r, err)
		return
	}
	sb, err := s.opts.Recall.BookAt(ctx, sample.ID, pos)
	var answers map[string]store.Confirmation
	if err == nil {
		answers, err = s.opts.Recall.Answers(ctx, sample.ID, pos)
	}
	var b report.Book
	if err == nil {
		b, err = s.opts.Reports.Book(ctx, sample.RunID, sb.BookID)
	}
	if err == nil && (b.Package == nil || b.Package.UUID != sb.UUID) {
		err = report.ErrIntegrity
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.recallError(w, r, err)
		return
	}
	v := &SampleBookView{SampleID: sample.ID, Position: pos, Size: sample.Size, Fields: sampleFields(b.Package.Calibre, answers),
		Excerpts: s.blindExcerpts(ctx, b.Package), AnswerURL: fmt.Sprintf("/sample/%d/book/%d", sample.ID, pos), Closed: sample.ClosedAt != nil}
	if pos > 1 {
		v.PrevURL = fmt.Sprintf("/sample/%d/book/%d", sample.ID, pos-1)
	}
	if pos < sample.Size {
		v.NextURL = fmt.Sprintf("/sample/%d/book/%d", sample.ID, pos+1)
	}
	s.render(w, 200, "sample_book.html", pageData{Title: fmt.Sprintf("Sample book %d of %d", pos, sample.Size), SignedIn: true, CSRFToken: sessionFrom(ctx).CSRFToken, SampleBook: v})
}

func (s *Server) sampleAndPosition(ctx context.Context, r *http.Request) (store.RecallSample, int, error) {
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		return store.RecallSample{}, 0, err
	}
	pos, err := positiveID(r.PathValue("n"))
	if err != nil || pos > 100000 {
		return store.RecallSample{}, 0, report.ErrInvalidFilter
	}
	sample, err := s.opts.Recall.Sample(ctx, id)
	if err != nil {
		return store.RecallSample{}, 0, err
	}
	if int(pos) > sample.Size {
		return store.RecallSample{}, 0, store.ErrNoSuchSample
	}
	return sample, int(pos), nil
}

// calibreText is Calibre's value of a field as the page shows it.
func calibreText(field string, c evidence.Record) string {
	var v string
	switch field {
	case "title":
		v = c.Title
	case "authors":
		v = strings.Join(c.Authors, " & ")
	case "publisher":
		v = c.Publisher
	case "pubdate":
		v = c.PubDate
	case "isbn":
		v = c.Identifiers["isbn"]
	case "languages":
		v = strings.Join(c.Languages, ", ")
	}
	return report.Display(v)
}

func sampleFields(c evidence.Record, answers map[string]store.Confirmation) []SampleField {
	out := make([]SampleField, 0, len(store.RecallFields))
	for i, name := range store.RecallFields {
		f := SampleField{Name: name, Label: fieldLabels[name], Calibre: calibreText(name, c), Answer: store.AnswerSkip, Key: i + 1}
		if name == "languages" {
			f.Hint = "Use language codes like deu, eng."
		}
		if a, ok := answers[name]; ok {
			f.Answer = a.Answer
			if a.Typed != nil {
				f.Typed = *a.Typed
			}
		}
		out = append(out, f)
	}
	return out
}

// blindExcerpts returns each format's front-matter text from the sealed,
// verified evidence, as plain text. The book page's view marks ISBN hints and
// labels the extractors' claims; those are findings in all but name, so they
// are dropped here and only the text is kept.
func (s *Server) blindExcerpts(ctx context.Context, p *evidence.Package) []SampleExcerpt {
	views := s.bookExcerpts(ctx, p)
	var out []SampleExcerpt
	for _, f := range p.Formats {
		v, ok := views[f.Format]
		if !ok {
			continue
		}
		e := SampleExcerpt{Format: f.Format, Chars: v.Chars, Truncated: v.Truncated, Complete: v.Complete, Available: v.Available}
		var text strings.Builder
		for _, part := range v.Parts {
			text.WriteString(part.Text)
		}
		e.Text = text.String()
		out = append(out, e)
	}
	return out
}

// confirmSample records the answers for one sampled book and moves on to the
// next. Every field must be answered once, and a typed value comes only with
// "Calibre wrong".
func (s *Server) confirmSample(w http.ResponseWriter, r *http.Request) {
	if s.recallOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportPageBudget)
	defer cancel()
	allowed := make([]string, 0, 2*len(store.RecallFields))
	for _, f := range store.RecallFields {
		allowed = append(allowed, "answer_"+f, "typed_"+f)
	}
	form, err := strictForm(r, "", allowed...)
	if err != nil {
		s.reportError(w, r, err)
		return
	}
	sample, pos, err := s.sampleAndPosition(ctx, r)
	if err != nil {
		s.recallError(w, r, err)
		return
	}
	answers := make([]store.Confirmation, 0, len(store.RecallFields))
	for _, f := range store.RecallFields {
		if !form.Has("answer_" + f) {
			s.reportError(w, r, report.ErrInvalidFilter)
			return
		}
		a := store.Confirmation{Field: f, Answer: form.Get("answer_" + f)}
		if t := strings.TrimSpace(form.Get("typed_" + f)); t != "" {
			a.Typed = &t
		}
		answers = append(answers, a)
	}
	if err := s.opts.Recall.Confirm(ctx, sample.ID, pos, answers, actor); err != nil {
		s.recallError(w, r, err)
		return
	}
	if pos >= sample.Size {
		http.Redirect(w, r, "/sample", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/sample/%d/book/%d", sample.ID, pos+1), http.StatusSeeOther)
}
