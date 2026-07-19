"""Repository portability regressions."""

from __future__ import annotations

import runpy
from pathlib import Path


def test_benchmark_epub_fallback_uses_repository_root() -> None:
    """Benchmark fixtures must not depend on a developer-specific checkout path."""

    repository_root = Path(__file__).resolve().parents[1]
    benchmark_module = repository_root / "tests/benchmarks/test_bench_infrastructure.py"
    source = benchmark_module.read_text(encoding="utf-8")

    namespace = runpy.run_path(str(benchmark_module))

    assert namespace["REPO_ROOT"] == repository_root
    assert 'Path("/home/' not in source
    assert "Path('/home/" not in source
    assert Path(namespace["TEST_EPUB"]).resolve().is_relative_to(repository_root)
