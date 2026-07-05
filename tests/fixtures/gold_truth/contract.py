"""
Gold-truth expected verdicts for the synthetic fixture library.

Each entry maps a fixture id to the EXACT expected per-field verdicts and overall action.
This file is the contract the ContentVerificationEngine must satisfy.
"""

from __future__ import annotations

# When the engine processes a fixture, the per-field verdict MUST equal
# (verdict_string, confidence_within_5) for every field.
# The overall action MUST equal expected_overall_action.
# auto_apply_eligible indicates whether the conservative-auto-apply gate should pass.

VERIFICATION_CONTRACT: dict[str, dict] = {
    "fix_001": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 90},
            "authors": {"verdict": "confirmed", "min_conf": 90},
            "isbn": {"verdict": "confirmed", "min_conf": 90},
        },
    },
    "fix_002": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "title": {"verdict": "missing", "min_conf": 90},
            "authors": {"verdict": "confirmed", "min_conf": 90},
            "isbn": {"verdict": "missing", "min_conf": 90},
        },
    },
    "fix_003": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "title": {"verdict": "mismatch", "min_conf": 90},
            "authors": {"verdict": "confirmed", "min_conf": 85},
        },
    },
    "fix_004": {
        "overall_action": "needs_review",
        "auto_apply": False,
        "risk_flags_present": ["author_swap"],
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 90},
            "authors": {"verdict": "mismatch", "min_conf": 80},
        },
    },
    "fix_005": {
        "overall_action": "needs_review",
        "auto_apply": False,
        "risk_flags_present": ["isbn_conflict"],
        "per_field": {
            "isbn": {"verdict": "mismatch", "min_conf": 85},
        },
    },
    "fix_006": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "publisher": {"verdict": "confirmed", "min_conf": 80},
        },
    },
    "fix_007": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "published_date": {"verdict": "confirmed", "min_conf": 80},
        },
    },
    "fix_008": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "language": {"verdict": "mismatch", "min_conf": 85},
            "authors": {"verdict": "mismatch", "min_conf": 80},
        },
    },
    "fix_009": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 70},
        },
    },
    "fix_010": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "authors": {"verdict": "mismatch", "min_conf": 90},
        },
    },
    "fix_011": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 85},
            "authors": {"verdict": "confirmed", "min_conf": 85},
        },
    },
    "fix_012": {
        "overall_action": "needs_review",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "ambiguous", "min_conf": 0},
            "authors": {"verdict": "ambiguous", "min_conf": 0},
        },
    },
    "fix_013": {
        "overall_action": "needs_review",
        "auto_apply": False,
        "per_field": {
            "series": {"verdict": "mismatch", "min_conf": 75},
        },
    },
    "fix_014": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "title": {"verdict": "mismatch", "min_conf": 75},
        },
    },
    "fix_015": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 85},
        },
    },
    "fix_016": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "title": {"verdict": "missing", "min_conf": 90},
            "authors": {"verdict": "missing", "min_conf": 90},
            "isbn": {"verdict": "missing", "min_conf": 85},
            "publisher": {"verdict": "missing", "min_conf": 70},
        },
    },
    "fix_017": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "title": {"verdict": "mismatch", "min_conf": 75},
        },
    },
    "fix_018": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "authors": {"verdict": "confirmed", "min_conf": 80},
        },
    },
    "fix_019": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "published_date": {"verdict": "confirmed", "min_conf": 80},
        },
    },
    "fix_020": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 85},
        },
    },
    "fix_021": {
        "overall_action": "needs_review",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "ambiguous", "min_conf": 0},
            "authors": {"verdict": "ambiguous", "min_conf": 0},
        },
    },
    "fix_022": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "isbn": {"verdict": "missing", "min_conf": 85},
            "publisher": {"verdict": "confirmed", "min_conf": 80},
        },
    },
    "fix_023": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 80},
            "series_index": {"verdict": "missing", "min_conf": 80},
        },
    },
    "fix_024": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 70},
            "authors": {"verdict": "confirmed", "min_conf": 70},
        },
    },
    "fix_025": {
        "overall_action": "needs_review",
        "auto_apply": False,
        "per_field": {
            "authors": {"verdict": "mismatch", "min_conf": 85},
            "publisher": {"verdict": "mismatch", "min_conf": 80},
        },
    },
    "fix_026": {
        "overall_action": "suggest_fix",
        "auto_apply": True,
        "per_field": {
            "title": {"verdict": "mismatch", "min_conf": 75},
        },
    },
    "fix_027": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "isbn": {"verdict": "confirmed", "min_conf": 80},
        },
    },
    "fix_028": {
        "overall_action": "needs_review",
        "auto_apply": False,
        "risk_flags_present": ["wrong_book"],
        "per_field": {
            "title": {"verdict": "mismatch", "min_conf": 90},
            "authors": {"verdict": "mismatch", "min_conf": 90},
        },
    },
    "fix_029": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 90},
        },
    },
    "fix_030": {
        "overall_action": "no_change",
        "auto_apply": False,
        "per_field": {
            "title": {"verdict": "confirmed", "min_conf": 90},
            "authors": {"verdict": "confirmed", "min_conf": 90},
            "isbn": {"verdict": "confirmed", "min_conf": 90},
            "publisher": {"verdict": "confirmed", "min_conf": 85},
            "published_date": {"verdict": "confirmed", "min_conf": 80},
            "language": {"verdict": "confirmed", "min_conf": 90},
        },
    },
}