## 📌 Description
Explain the changes you made and the rationale behind them. Reference any related issues (e.g. `Fixes #123`).

## 🛠️ Type of Change
- [ ] 🐛 Bug fix (non-breaking change which fixes an issue)
- [ ] ✨ New feature (non-breaking change which adds functionality)
- [ ] ⚡ Performance improvement
- [ ] 🛡️ Security / Hardening improvement
- [ ] 📝 Documentation update
- [ ] 🧹 Refactor / Code cleanliness

## 🧪 Verification & Testing
Describe the tests you ran to verify your changes:
- [ ] `uv run ruff check .` passed with 0 errors
- [ ] `uv run ruff format --check .` passed
- [ ] `uv run mypy src` passed with strict mode (0 errors)
- [ ] `uv run pytest` test suite passed locally

## 🛡️ Safety Checklist
- [ ] No personal libraries, copyrighted texts, or secret API keys are included.
- [ ] Read-only guarantees on offline libraries are preserved.
- [ ] Database mutations strictly enforce atomic snapshots and transaction boundaries.
