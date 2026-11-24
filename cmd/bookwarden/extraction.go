package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/felixapel/calibre-bookwarden/internal/agent"
	"github.com/felixapel/calibre-bookwarden/internal/config"
	"github.com/felixapel/calibre-bookwarden/internal/extraction"
	"github.com/felixapel/calibre-bookwarden/internal/extractor"
	"github.com/felixapel/calibre-bookwarden/internal/fix"
	"github.com/felixapel/calibre-bookwarden/internal/store"
)

// extractionService builds M4's shadow extraction from the configured
// extractors, or returns nil when fewer than two are configured: agreement
// needs two families (design section 7). The extractors get their own quota
// state, apart from the panel's seats, which may share their names; their
// calls are counted in calls like the panel's.
func extractionService(cfg config.ExtractorConfig, cache extraction.Cache, calls agent.CallRecorder, logger *slog.Logger) (*extraction.Service, error) {
	if len(cfg.Extractors) < 2 {
		if len(cfg.Extractors) == 1 {
			logger.Warn("extraction needs at least two extractors; it is off", "extractors", cfg.Extractors[0].Name)
		}
		logger.Info("extraction=off")
		return nil, nil
	}
	quotas := agent.NewQuotas()
	c := extraction.Config{Hosts: map[string]string{}, MaxCalls: cfg.MaxCalls, Cache: cache, Quotas: quotas}
	var names []string
	for _, sc := range cfg.Extractors {
		hc, err := agent.NewSeatClient(sc.URL, sc.AllowCIDR)
		if err != nil {
			return nil, fmt.Errorf("extractor %s: %w", sc.Name, err)
		}
		e, err := extractor.New(sc, hc, agent.WithQuotas(quotas), agent.WithCalls(calls))
		if err != nil {
			return nil, fmt.Errorf("extractor %s: %w", sc.Name, err)
		}
		u, err := url.Parse(sc.URL)
		if err != nil {
			return nil, fmt.Errorf("extractor %s: %w", sc.Name, err)
		}
		c.Extractors = append(c.Extractors, e)
		c.Hosts[sc.Name] = u.Host
		names = append(names, sc.Name)
	}
	logger.Info("extraction=on", "extractors", strings.Join(names, ","))
	return extraction.New(c), nil
}

// textCheck re-verifies a book's sealed extractions from the stored excerpt
// and raw answers, with the function verify-seals uses, before a fix that
// rests on the printed text is approved or applied (ADR-036 section 5). A
// mismatch, a missing answer, an extraction sealed under other rules and a
// book with no extraction all refuse; only a failure to read returns another
// error.
func textCheck(repo *store.ReportRepo) fix.TextCheck {
	return func(ctx context.Context, runID int64, bookID int) error {
		checked := 0
		err := repo.BookExtractions(ctx, runID, bookID, func(x store.ExtractionRef) error {
			checked++
			if x.Missing {
				return fmt.Errorf("%w: the excerpt or a raw answer is missing", fix.ErrTextUnverified)
			}
			err := extraction.Reverify(x.Excerpt, x.Body, x.Extraction, x.Raws)
			switch {
			case errors.Is(err, extraction.ErrOtherVersion):
				return fix.ErrTextOldVerifier
			case err != nil:
				return fmt.Errorf("%w: %w", fix.ErrTextUnverified, err)
			}
			return nil
		})
		switch {
		case err != nil:
			return err
		case checked == 0:
			return fmt.Errorf("%w: the book has no extraction", fix.ErrTextUnverified)
		}
		return nil
	}
}
