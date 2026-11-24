package sources

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/felixapel/calibre-bookwarden/internal/evidence"
)

// sruResponse is the part of an SRU searchRetrieveResponse bookwarden reads.
// Tags without a namespace match any, so SRU 1.1 and 1.2 both decode.
type sruResponse struct {
	XMLName         xml.Name `xml:"searchRetrieveResponse"`
	NumberOfRecords string   `xml:"numberOfRecords"`
	Records         []struct {
		Data struct {
			Record marcRecord `xml:"record"`
		} `xml:"recordData"`
	} `xml:"records>record"`
	Diagnostics *struct{} `xml:"diagnostics"`
}

type marcRecord struct {
	Control []struct {
		Tag   string `xml:"tag,attr"`
		Value string `xml:",chardata"`
	} `xml:"controlfield"`
	Data []marcField `xml:"datafield"`
}

type marcField struct {
	Tag  string `xml:"tag,attr"`
	Ind1 string `xml:"ind1,attr"`
	Ind2 string `xml:"ind2,attr"`
	Subs []struct {
		Code  string `xml:"code,attr"`
		Value string `xml:",chardata"`
	} `xml:"subfield"`
}

func (f marcField) sub(code string) string {
	for _, s := range f.Subs {
		if s.Code == code {
			return s.Value
		}
	}
	return ""
}

func (f marcField) has(code string) bool {
	for _, s := range f.Subs {
		if s.Code == code {
			return true
		}
	}
	return false
}

func (f marcField) all(code string) []string {
	var out []string
	for _, s := range f.Subs {
		if s.Code == code {
			out = append(out, s.Value)
		}
	}
	return out
}

func (r marcRecord) control(tag string) string {
	for _, c := range r.Control {
		if c.Tag == tag {
			return c.Value
		}
	}
	return ""
}

func (r marcRecord) fields(tag string) []marcField {
	var out []marcField
	for _, f := range r.Data {
		if f.Tag == tag {
			out = append(out, f)
		}
	}
	return out
}

const maxDepth = 64

var errHostile = errors.New("sources: hostile or malformed XML")

// checkXML refuses DTDs, entity declarations and nesting deeper than 64
// before the structured decode.
func checkXML(body []byte) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	depth := 0
	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errHostile
		}
		switch tok.(type) {
		case xml.Directive:
			return errHostile
		case xml.StartElement:
			if depth++; depth > maxDepth {
				return errHostile
			}
		case xml.EndElement:
			depth--
		}
	}
}

// parseSRU returns the MARC records of an SRU answer and the number of matches
// the server reports, or an error when the answer is malformed, hostile or an
// SRU diagnostic.
func parseSRU(body []byte) ([]marcRecord, int, error) {
	if err := checkXML(body); err != nil {
		return nil, 0, err
	}
	var resp sruResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, 0, errHostile
	}
	if resp.Diagnostics != nil {
		return nil, 0, errors.New("sources: SRU diagnostic")
	}
	total, err := strconv.Atoi(strings.TrimSpace(resp.NumberOfRecords))
	if err != nil {
		return nil, 0, errHostile
	}
	out := make([]marcRecord, 0, len(resp.Records))
	for _, r := range resp.Records {
		out = append(out, r.Data.Record)
	}
	return out, total, nil
}

var year = regexp.MustCompile(`\d{4}`)

// isbdTrim removes ISBD trailing punctuation and surrounding brackets.
func isbdTrim(s string) string {
	s = text(s)
	for {
		before := s
		for _, suffix := range []string{" /", " :", " ;", " =", ",", "."} {
			s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
		if s == before {
			return s
		}
	}
}

// heading cleans a MARC personal name: a trailing comma always goes, a
// trailing period only after a word of three or more letters, so initials
// ("J. K.") and "Jr." keep theirs. An inverted heading (first indicator 1)
// with exactly one comma becomes "Forenames Surname".
func heading(s, ind1 string) string {
	s = strings.TrimSpace(strings.TrimSuffix(text(s), ","))
	if strings.HasSuffix(s, ".") {
		words := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
		if last := words[len(words)-1]; utf8.RuneCountInString(last) > 3 {
			s = strings.TrimSuffix(s, ".")
		}
	}
	if ind1 == "1" && strings.Count(s, ",") == 1 {
		surname, forenames, _ := strings.Cut(s, ",")
		s = strings.TrimSpace(forenames) + " " + strings.TrimSpace(surname)
	}
	return text(s)
}

// marcSource maps one MARC record to a SourceRecord.
func marcSource(r marcRecord) evidence.SourceRecord {
	rec := evidence.SourceRecord{ID: id(r.control("001"))}
	for _, f := range r.fields("020") {
		for _, code := range []string{"a", "9"} {
			for _, v := range f.all(code) {
				first, _, _ := strings.Cut(strings.TrimSpace(v), " ")
				rec.ISBNs = appendISBN(rec.ISBNs, first)
			}
		}
	}
	if f := r.fields("245"); len(f) > 0 {
		rec.Title = isbdTrim(f[0].sub("a"))
		rec.Subtitle = isbdTrim(f[0].sub("b"))
	}
	for _, f := range r.fields("100") {
		rec.Authors = appendText(rec.Authors, heading(f.sub("a"), f.Ind1))
	}
	for _, f := range r.fields("700") {
		relator := f.all("4")
		if f.has("t") || (len(relator) == 0 && f.has("e")) || (len(relator) > 0 && !contains(relator, "aut")) {
			continue
		}
		rec.Authors = appendText(rec.Authors, heading(f.sub("a"), f.Ind1))
	}
	for _, f := range r.fields("041") {
		for _, code := range f.all("a") {
			for len(code) >= 3 && len(code)%3 == 0 {
				rec.Languages = appendText(rec.Languages, lang(code[:3]))
				code = code[3:]
			}
		}
	}
	c008 := r.control("008")
	if len(r.fields("041")) == 0 && len(c008) >= 38 {
		rec.Languages = appendText(rec.Languages, lang(c008[35:38]))
	}
	if len(c008) >= 11 && isYear(c008[7:11]) {
		rec.Date = c008[7:11]
	}
	for _, tag := range []string{"264", "260"} {
		for _, f := range r.fields(tag) {
			if (tag == "264" && f.Ind2 != "1") || rec.Publisher != "" {
				continue
			}
			rec.Publisher = isbdTrim(f.sub("b"))
			if rec.Date == "" {
				rec.Date = year.FindString(f.sub("c"))
			}
		}
	}
	return rec
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if strings.TrimSpace(v) == s {
			return true
		}
	}
	return false
}

func isYear(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
