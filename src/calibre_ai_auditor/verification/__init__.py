"""Verification subsystem: per-field adjudication of Calibre metadata vs book content."""

from . import host_registry, ocr_router, restore
from .engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
    build_observation_from_extraction,
)
from .engine_llm import (
    LLMWitness,
    WitnessCache,
    WitnessCacheEntry,
    WitnessConfig,
    WitnessResult,
    WITNESS_SCHEMA,
    build_witness_prompt,
)
from .verdict import (
    AUTO_APPLY_MIN_CONFIDENCE,
    AUTO_APPLY_MIN_FIELD_CONFIDENCE,
    HIGH_RISK_FLAGS,
    BookVerdict,
    EvidenceSpan,
    FieldVerdict,
    JudgeCall,
    VerdictAction,
    VerdictKind,
)

__all__ = [
    "AUTO_APPLY_MIN_CONFIDENCE",
    "AUTO_APPLY_MIN_FIELD_CONFIDENCE",
    "HIGH_RISK_FLAGS",
    "BookVerdict",
    "EvidenceSpan",
    "FieldVerdict",
    "JudgeCall",
    "VerdictAction",
    "VerdictKind",
    "ContentVerificationEngine",
    "DeclaredMetadata",
    "ObservationSet",
    "build_observation_from_extraction",
    "ocr_router",
    "host_registry",
    "restore",
    "RestorePoint",
    "RestorePointStore",
    "ConservativeAutoApply",
    "LLMWitness",
    "WitnessCache",
    "WitnessCacheEntry",
    "WitnessConfig",
    "WitnessResult",
    "WITNESS_SCHEMA",
    "build_witness_prompt",
]