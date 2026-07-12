# Metadata-only 50k scale gate

Run from the repository root:

```bash
.venv/bin/python scripts/benchmark_50k_metadata.py
```

The gate exercises `ContentVerificationEngine.verify` for 50,000 deterministic
records. It deliberately excludes Calibre scan I/O, extraction, OCR, providers,
LLM calls, and database persistence; results must not be presented as an
end-to-end library audit time.

Observed on 2026-07-12:

| Metric | Result | Gate |
|---|---:|---:|
| Records | 50,000 | 50,000 |
| Elapsed | 2.897 s | < 1,800 s |
| Throughput | 17,257.18 records/s | informational |
| Maximum RSS | 475.17 MiB | < 2,048 MiB |

Environment: Python 3.12.13, Linux 7.1.3-2-cachyos x86_64, glibc 2.43. The
script exits non-zero when either release gate is exceeded.
