package extractor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/extract"
)

func TestParseOutput(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []extract.Claim
		wantErr bool
	}{
		{"plain object", `{"claims":[{"field":"title","value":"The X","quote":"THE X"}]}`,
			[]extract.Claim{{Field: "title", Value: "The X", Quote: "THE X"}}, false},
		{"fenced", "```json\n{\"claims\":[{\"field\":\"year\",\"value\":\"2019\",\"quote\":\"© 2019\",\"label\":\"Copyright\"}]}\n```",
			[]extract.Claim{{Field: "year", Value: "2019", Quote: "© 2019", Label: "copyright"}}, false},
		{"role normalised", `{"claims":[{"field":"contributor","value":"Jane Doe","quote":"by Jane Doe","role":" Author "}]}`,
			[]extract.Claim{{Field: "contributor", Value: "Jane Doe", Quote: "by Jane Doe", Role: "author"}}, false},
		{"empty", `{"claims":[]}`, nil, false},
		{"prose before", "Here you go:\n{\"claims\":[]}", nil, true},
		{"two objects", `{"claims":[]}{"claims":[]}`, nil, true},
		{"not json", "no", nil, true},
		{"claims missing", `{"items":[]}`, nil, true},
		{"empty reply", "", nil, true},
		// A number is kept as its literal text; the verifier judges it.
		{"value a number", `{"claims":[{"field":"series_index","value":6,"quote":"Book 6 of the Salt Road Cycle"},{"field":"series_index","value":2.50,"quote":"Book 2.50"}]}`,
			[]extract.Claim{{Field: "series_index", Value: "6", Quote: "Book 6 of the Salt Road Cycle"}, {Field: "series_index", Value: "2.50", Quote: "Book 2.50"}}, false},
		{"value true", `{"claims":[{"field":"year","value":true,"quote":"© 2019"}]}`, nil, true},
		{"value an object", `{"claims":[{"field":"year","value":{"y":2019},"quote":"© 2019"}]}`, nil, true},
		{"value a list", `{"claims":[{"field":"year","value":["2019"],"quote":"© 2019"}]}`, nil, true},
		{"quote a number", `{"claims":[{"field":"year","value":"2019","quote":2019}]}`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOutput(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Errorf("err = %v, want ErrInvalidOutput", err)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("claims = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("claim %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseOutputBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"claims":[`)
	for i := range maxClaims + 1 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"field":"title","value":"x","quote":"x"}`)
	}
	b.WriteString(`]}`)
	if _, err := ParseOutput(b.String()); !errors.Is(err, ErrInvalidOutput) {
		t.Errorf("too many claims: err = %v", err)
	}
	// Trailing text after a long answer, past the decoder's buffer.
	pad := strings.Repeat(" ", 8192)
	if _, err := ParseOutput(`{"claims":[]}` + pad + "trailing words"); !errors.Is(err, ErrInvalidOutput) {
		t.Errorf("trailing text after the buffer: err = %v", err)
	}
	long := strings.Repeat("a", maxClaimRunes+1)
	if _, err := ParseOutput(`{"claims":[{"field":"title","value":"` + long + `","quote":"x"}]}`); !errors.Is(err, ErrInvalidOutput) {
		t.Errorf("long value: err = %v", err)
	}
}

func TestBuildInput(t *testing.T) {
	nonceSource = func() (string, error) { return "n0nce", nil }
	t.Cleanup(func() { nonceSource = defaultNonce })
	in, err := buildInput("THE X\nby Jane Doe")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in, "<<<DATA-n0nce\nTHE X\nby Jane Doe\nDATA-n0nce>>>") {
		t.Errorf("input = %q", in)
	}
	if _, err := buildInput("text with DATA-n0nce>>> inside"); err == nil {
		t.Error("a marker in the data must fail closed")
	}
}

type fakeSeat struct {
	text string
	err  error
	got  string
}

func (f *fakeSeat) Name() string  { return "deepseek" }
func (f *fakeSeat) Model() string { return "m" }
func (f *fakeSeat) Judge(_ context.Context, brief, packet string) (agent.Reply, error) {
	f.got = brief + "\n" + packet
	return agent.Reply{Text: f.text, InputTokens: 9, OutputTokens: 3, Model: "m-2026"}, f.err
}

func TestExtract(t *testing.T) {
	x := extract.Excerpt{Text: "THE LANTERN YEARS\nby Jane Doe"}
	seat := &fakeSeat{text: `{"claims":[{"field":"title","value":"THE LANTERN YEARS","quote":"THE LANTERN YEARS"}]}`}
	e := Extractor{Name: "deepseek", Family: "deepseek", Model: "m", seat: seat}
	r, err := e.Extract(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Claims) != 1 || r.Claims[0].Source != "deepseek" || r.Reply.Model != "m-2026" || r.Raw == "" {
		t.Errorf("result = %+v", r)
	}
	if !strings.Contains(seat.got, SystemPrompt) || !strings.Contains(seat.got, "THE LANTERN YEARS") {
		t.Error("the prompt and the excerpt must be sent")
	}
	// The model never sees anything but the excerpt: no record, no book id.
	if strings.Contains(seat.got, "calibre") {
		t.Error("unexpected context in the request")
	}

	bad := Extractor{Name: "glm", Model: "m", seat: &fakeSeat{text: "sorry"}}
	if _, err := bad.Extract(context.Background(), x); !errors.Is(err, ErrInvalidOutput) {
		t.Errorf("invalid output: err = %v", err)
	}
	failing := Extractor{Name: "gemini", seat: &fakeSeat{err: errors.New("agent: seat gemini: quota_exhausted")}}
	if _, err := failing.Extract(context.Background(), x); err == nil {
		t.Error("a seat error must surface")
	}
}

func TestServedBy(t *testing.T) {
	for _, tc := range []struct {
		requested, reported string
		want                bool
	}{
		{"glm-5.3-flash", "glm-5.3-flash", true},
		{"opencode-go/glm-5.3-flash", "glm-5.3-flash", true},
		{"opencode-go/glm-5.3-flash", "opencode-go/glm-5.3-flash", true},
		{"claude-haiku-5-5", "claude-haiku-5-5-20261001", true},
		{"deepseek-v4.1-flash", "deepseek-v4.1-flash-2026-10", true},
		{"glm-5.3-flash", "glm-5.3-flash-2026-10-01", true},
		// A size or a variant is another model, not a dated snapshot.
		{"opencode-go/hy3", "hy3-7b-instruct", false},
		{"opencode-go/qwen3", "qwen3-235b-a22b", false},
		{"opencode-go/glm-5.3-flash", "glm-5.3-flash-2", false},
		{"glm-5.3-flash", "glm-5.3-flash-2026-10-preview", false},
		{"opencode-go/deepseek-v4.1-flash", "openai/gpt-oss-120b", false}, // a gateway fallback
		{"opencode-go/deepseek-v4.1-flash", "", false},
		{"qwen3.8", "qwen3.8-flash", false},
		{"glm-5.3-flash", "glm-5.3-flash-free", false},
		{"glm-5.3-flash", "zai/glm-5.3-flash", false},
		{"", "glm-5.3-flash", false},
	} {
		if got := ServedBy(tc.requested, tc.reported); got != tc.want {
			t.Errorf("ServedBy(%q, %q) = %v", tc.requested, tc.reported, got)
		}
	}
}

func TestExtractRefusesAnotherModel(t *testing.T) {
	x := extract.Excerpt{Text: "THE LANTERN YEARS"}
	seat := &fakeSeat{text: `{"claims":[{"field":"title","value":"THE LANTERN YEARS","quote":"THE LANTERN YEARS"}]}`}
	e := Extractor{Name: "deepseek", Model: "opencode-go/deepseek-v4.1-flash", seat: seat}
	r, err := e.Extract(context.Background(), x)
	if !errors.Is(err, ErrModelMismatch) || len(r.Claims) != 0 || r.Reply.Model != "m-2026" {
		t.Fatalf("Extract = %+v, %v; want ErrModelMismatch, no claims, the reported model kept", r, err)
	}
}
