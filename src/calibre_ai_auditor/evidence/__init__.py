"""v0.9 evidence package — DEPRECATED, superseded by `verification.engine`.

The v0.9 evidence-first flow (build_evidence_package → MetadataJudge →
apply_confidence_thresholds → EvidencePackage) has been fully replaced by
the v1.0 ContentVerificationEngine + LLMWitness + BookVerdict pipeline.

This module remains only to provide a single import surface for code paths
that have not yet migrated (notably `web/api/inspect.py` which still imports
`build_evidence_package`). New code MUST use `calibre_ai_auditor.verification`
directly.

The shim re-exports the v0.9 types so existing imports continue to work
during the migration window. After v2.0 this module will be deleted.

See:
  - src/calibre_ai_auditor/verification/verdict.py — v1.0 FieldVerdict
  - src/calibre_ai_auditor/verification/engine.py — v1.0 ContentVerificationEngine
"""

# All v0.9 evidence primitives have been deleted. Any code still importing
# from this module is using the legacy path. Use the v1.0 verification
# subsystem instead.
__all__: list[str] = []
DEPRECATION_NOTICE = (
    "calibre_ai_auditor.evidence is deprecated as of v1.0; "
    "use calibre_ai_auditor.verification instead."
)