package metrics

import "slices"

// Snapshot is what the database says, read once per scrape.
type Snapshot struct {
	// RunsByStatus counts the stored runs by status. Retention prunes old
	// runs, so these are not lifetime counters.
	RunsByStatus map[string]int
	// LastRun is the last finished run; nil before the first one.
	LastRun *LastRun
	// ProposalsOpen counts, by field, the last finished run's proposals that
	// nobody has decided.
	ProposalsOpen map[string]int
	// FixesApplied and FixesUndone count completed operations in the ledger,
	// which is append-only.
	FixesApplied, FixesUndone int
	IncidentsOpen             int
	// SealChecksBroken counts runs whose latest seal check found damage.
	SealChecksBroken int
	Labels           []LabelCount
}

// LastRun is the last finished run.
type LastRun struct {
	DurationSeconds float64
	Kind            string         // full, incremental or book
	Books           map[string]int // by book state
}

// LabelCount is the precision labels of one field and kind (fill or
// replace), summed over evidence paths.
type LabelCount struct {
	Field, Kind    string
	Labels, Misses int
}

// RunStatuses and BookStates are always written, with zero for none.
var (
	RunStatuses = []string{"running", "waiting", "completed", "cancelled", "failed"}
	BookStates  = []string{"pending", "checked", "source_changed", "failed"}
)

// BuildInfo writes bookwarden_build_info.
func (w *Writer) BuildInfo(version string) {
	w.Family("bookwarden_build_info", "A constant 1, labelled with the bookwarden version.", "gauge")
	w.Sample("bookwarden_build_info", 1, "version", version)
}

// WriteSnapshot writes the database-backed metrics.
func WriteSnapshot(w *Writer, s Snapshot) {
	w.Family("bookwarden_runs", "Stored runs by status; retention prunes old runs, so it can fall.", "gauge")
	for _, st := range RunStatuses {
		w.Sample("bookwarden_runs", float64(s.RunsByStatus[st]), "status", st)
	}
	if s.LastRun != nil {
		w.Family("bookwarden_run_duration_seconds", "How long the last finished run took, labelled with its kind.", "gauge")
		w.Sample("bookwarden_run_duration_seconds", s.LastRun.DurationSeconds, "kind", s.LastRun.Kind)
		w.Family("bookwarden_run_books", "Books of the last finished run by state.", "gauge")
		for _, st := range BookStates {
			w.Sample("bookwarden_run_books", float64(s.LastRun.Books[st]), "state", st)
		}
	}
	w.Family("bookwarden_proposals_open", "Undecided proposals of the last finished run by field.", "gauge")
	for _, f := range sortedKeys(s.ProposalsOpen) {
		w.Sample("bookwarden_proposals_open", float64(s.ProposalsOpen[f]), "field", f)
	}
	w.Family("bookwarden_fixes_applied_total", "Completed apply operations in the write ledger.", "counter")
	w.Sample("bookwarden_fixes_applied_total", float64(s.FixesApplied))
	w.Family("bookwarden_fixes_undone_total", "Completed undo operations in the write ledger.", "counter")
	w.Sample("bookwarden_fixes_undone_total", float64(s.FixesUndone))
	w.Family("bookwarden_incidents_open", "Unacknowledged write incidents; one pauses all writes.", "gauge")
	w.Sample("bookwarden_incidents_open", float64(s.IncidentsOpen))
	w.Family("bookwarden_seal_checks_broken", "Runs whose latest seal check found damage.", "gauge")
	w.Sample("bookwarden_seal_checks_broken", float64(s.SealChecksBroken))

	labels := slices.Clone(s.Labels)
	slices.SortFunc(labels, func(a, b LabelCount) int {
		return slices.Compare([]string{a.Field, a.Kind}, []string{b.Field, b.Kind})
	})
	w.Family("bookwarden_labels", "Precision labels (blind individual decisions) by field and kind.", "gauge")
	for _, l := range labels {
		w.Sample("bookwarden_labels", float64(l.Labels), "field", l.Field, "kind", l.Kind)
	}
	w.Family("bookwarden_label_misses", "Precision labels that were misses (rejected) by field and kind.", "gauge")
	for _, l := range labels {
		w.Sample("bookwarden_label_misses", float64(l.Misses), "field", l.Field, "kind", l.Kind)
	}
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
