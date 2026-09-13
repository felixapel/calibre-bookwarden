# ADR-007: Mathematical Cover Quality Scoring (CQS 0-100) and Defect Detection

## Status
Accepted

## Date
2026-08-25

## Context
Ebook libraries often suffer from degraded cover art: low-resolution thumbnail stubs (e.g. 150x200 pixels), generic brown Calibre default templates, blank scanner pages, and extreme decompression bombs (>30 Megapixels). Relying on multimodal LLMs (such as Gemini Vision) to grade every single cover is prohibitively slow, incurs high token costs, and creates external network dependencies.

## Decision
Implement a deterministic mathematical scoring engine (`CoverQualityScorer` in `covers/scorer.py`) operating entirely locally using Pillow and NumPy without neural network dependencies:
- **Quality Score (CQS 0–100)**: Evaluates four core 25-point dimensions:
  1. *Dimension Score (0–25)*: Proximity to ideal e-reader dimensions (1200x1800 px).
  2. *Aspect Ratio Score (0–25)*: Proximity to canonical 1:1.5 (2:3) book proportions.
  3. *Sharpness Score (0–25)*: Calculated via discrete 3x3 Laplacian edge convolution variance.
  4. *Contrast & Information Score (0–25)*: Shannon information entropy (\(0.0 \le H \le 8.0\)) and dynamic range.
- **Classification Tiers**:
  - `Tier S / Tier A` (80–100): Crisp high-definition cover.
  - `Tier B` (60–79): Acceptable quality.
  - `Tier C` (40–59): Low resolution or noticeable blur. Candidate for upgrade.
  - `Tier D` (<40): Severe defect, tiny thumbnail, or spurious image.
- **Spurious Defect Detection**: Detects solid monochromatic blocks, aspect ratio anomalies (>2.2 or <1.1), and blank scanning artifacts.
- **Safety Invariant**: Strictly enforces `Image.MAX_IMAGE_PIXELS = 60_000_000` to prevent memory exhaustion from decompression bomb exploits.

## Consequences
- Fast evaluation: grades over 150 covers per second on a single CPU core.
- Fully offline and deterministic: reproducible grades across platforms with zero external API calls.
- Provides actionable signals (`is_actionable: bool`) for automated optimization and visual triage.
