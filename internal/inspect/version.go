package inspect

// Version identifies the behaviour of everything that turns a downloaded file
// into the file evidence of a sealed package. An incremental run reuses a
// book's file evidence only from a package made under the same Version
// (ADR-039), so bump it whenever a change could make a fresh check of an
// unchanged file seal different file evidence.
//
// What counts, besides this package and its pdf subpackage:
//   - package ocr: the page ranges, the OCR and text-layer reads, the
//     excerpts;
//   - package findings' ISBN scanner (ScanISBNs, MaxISBNs, the qualifier and
//     the checksum and range tables), because it decides which ISBNs the
//     sealed hints hold;
//   - the file-evidence half of package verify (fileevidence.go): how a
//     format and a cover are exported, measured and recorded.
//
// Do not bump it for changes that leave the evidence of every file as it was
// (a refactor, logging, performance). Findings and decisions are recomputed on
// every run and need no bump.
//
// TestSourcesMatchTheVersion pins a hash of those sources next to the version
// it was last decided for. When it fails, decide whether the change can alter
// file evidence: if so, bump Version here; then paste the new hash and the
// version into the test.
//
// Not part of the version, because verify's inspector key already carries
// them: the limits, the Go toolchain and the golang.org/x/image and
// golang.org/x/text versions, and the versions of pdftotext and tesseract and
// the tessdata commit.
//
// A bump costs one slow run: the next incremental run checks every book in
// full. Forgetting one costs the reverse: unchanged books keep file evidence
// from the old behaviour until it expires (verify.ReuseMaxAge, 8 days).
const Version = 2
