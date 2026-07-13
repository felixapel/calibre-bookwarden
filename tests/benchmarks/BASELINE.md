# Test & benchmark baseline — calibre-ai-auditor v1.0

Captured baseline numbers for the v1.0 verification engine. These numbers
serve as the regression baseline for future PRs. The benchmark suite is
opt-in (`pytest --benchmark-only`) so the regular `pytest` run stays fast.

## Environment

Captured on: Linux 6.17 x86_64, Python 3.14.6, single core (dev container)

## Per-rule latency (1000 calls per benchmark)

| Rule | Median | Notes |
|------|--------|-------|
| `verify_title` (realistic) | 19.8ms/1000 | Series-aware edition-tag stripping, fuzzy match |
| `verify_title` (mismatch) | 19.9ms/1000 | Same path, different similarity band |
| `verify_authors` (match) | 3.6ms/1000 | Set comparison |
| `verify_authors` (transliteration) | ~25ms/1000 | Cross-script surname match (Cyrillic ↔ Latin) |
| `verify_isbn` (match) | 1.7ms/1000 | Checksum-validated exact match |
| `verify_isbn` (invalid checksum) | 1.7ms/1000 | Fall-through path |
| `verify_publisher` | 11.0ms/1000 | Fuzzy + substring variant handling |
| `verify_published_date` | 2.6ms/1000 | Year-only tolerance |
| `verify_language` | 2.0ms/1000 | 3-letter ISO code match |
| `verify_series` | 1.6ms/1000 | Light path (no header sample) |
| `verify_series_index` | 1.7ms/1000 | Float comparison |
| **All 8 rules combined** | ~50ms/100 books | Full per-book deterministic check |

## Engine throughput

| Corpus size | Books/sec | Total wall time |
|-------------|-----------|----------------|
| 30 fixtures (regression) | ~14,300 | 2.1ms |
| 100 books | ~8,600 | 11.5ms |
| 1,000 books | ~8,500 | 118ms |
| 10,000 books | ~8,300 | 1.2s |
| Scaling check (1k→10k) | linear within 2x | — |

**Verdict: 50k-book audit = ~6 seconds pure engine time on this hardware.**

## LLM witness

| Operation | Median | Notes |
|-----------|--------|-------|
| Cache hit | 107µs | No LLM call |
| Cache miss (50ms simulated LLM) | 87µs | Dominated by `asyncio.run` overhead in mock |
| Cache miss (500ms simulated LLM) | 114µs | Same — `asyncio.run` overhead dominates |
| Cache put+get (1000 ops) | 4ms | In-memory, <5µs per op |
| Prompt build (1000 prompts) | 1.4ms | <2µs per prompt |

**Real-world production speedup with cache: ~10,000x** (107µs vs ~1s LLM call).

## Metrics

| Metrics count | Render time |
|---------------|-------------|
| 0 (empty) | 561ns |
| 100 | 102µs |
| 1,000 | 1.6ms |
| 10,000 | 5.5ms |
| 100k inc | 42ms (2.4M ops/sec) |

## Restore points

| Operation | Time | Scale |
|-----------|------|-------|
| Create 1000 restore points | 58ms | 58µs each |
| List all 1000 | 18ms | — |
| Cleanup 1000 non-expired | 17ms | — |
| Resumable state 50k books | 7.7ms | — |

## Host discovery

| Operation | Time | Notes |
|-----------|------|-------|
| Static config build | 1.5µs | No I/O |
| Live health check (3 hosts) | 23ms | Real network, all 3 reachable |

## Content extraction

| File size | Single extraction | 200 extractions (amortized) |
|-----------|-------------------|------------------------------|
| 50 MB EPUB | 5.3ms | 5.4ms |

## OCR comparison (Tesseract only — PaddleOCR/Surya not in this env)

| File | Provider | ms/page | chars | accuracy |
|------|----------|---------|-------|----------|
| clean_text.pdf | tesseract | 438 | 26 | 11.3% |
| multilingual.pdf | tesseract | 437 | 26 | 12.7% |
| noisy.pdf | tesseract | 424 | 26 | 21.9% |

Note: accuracy is low because the synthetic corpus uses insert_text which places glyphs
as vector graphics that OCR can't easily reconstruct. Real scanned PDFs would show much higher accuracy.

## How to use these baselines

```bash
# Run benchmarks and compare against saved baseline
pytest tests/benchmarks/ --benchmark-only \
       --benchmark-compare=.benchmarks/baseline.json

# Save a new baseline (only after intentional perf changes)
pytest tests/benchmarks/ --benchmark-only \
       --benchmark-autosave
```

CI runs benchmarks on `push:main` and uploads the JSON as artifact.