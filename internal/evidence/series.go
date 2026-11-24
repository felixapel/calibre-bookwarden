package evidence

import (
	"fmt"

	"github.com/felixapel/calibre-bookwarden/internal/seriesindex"
)

// checkSeries refuses series fields in a package of schema 4 or earlier, which
// can't have been sealed with them, and a series index that is not in the
// canonical form seriesindex.Normalize writes.
func (p Package) checkSeries() error {
	check := func(where, series, index string) error {
		if n, ok := seriesindex.Normalize(index); !ok || n != index {
			return fmt.Errorf("%w: %s has the series index %q", ErrInvalidEvidence, where, index)
		}
		if series == "" && index != "" {
			return fmt.Errorf("%w: %s has a series index and no series", ErrInvalidEvidence, where)
		}
		return nil
	}
	has := p.Calibre.Series != "" || p.Calibre.SeriesIndex != ""
	if err := check("the Calibre record", p.Calibre.Series, p.Calibre.SeriesIndex); err != nil {
		return err
	}
	for _, f := range p.Formats {
		if m := f.Metadata; m != nil {
			has = has || m.Series != "" || m.SeriesIndex != "" || m.Limits != nil && (m.Limits.Series || m.Limits.SeriesIndex)
			if err := check(f.Format+" metadata", m.Series, m.SeriesIndex); err != nil {
				return err
			}
			if m.Series == "" && m.Limits != nil && (m.Limits.Series || m.Limits.SeriesIndex) {
				return fmt.Errorf("%w: %s metadata has a series limit and no series", ErrInvalidEvidence, f.Format)
			}
		}
	}
	for _, l := range p.Lookups {
		for _, r := range l.Results {
			for _, rec := range r.Records {
				has = has || rec.Series != "" || rec.SeriesIndex != ""
				if err := check(r.Source+" record "+rec.ID, rec.Series, rec.SeriesIndex); err != nil {
					return err
				}
			}
		}
	}
	if has && p.SchemaVersion < SchemaVersionV5 {
		return fmt.Errorf("%w: schema %d holds a schema 5 series field", ErrInvalidEvidence, p.SchemaVersion)
	}
	return nil
}
