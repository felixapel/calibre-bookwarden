from calibre_ai_auditor.storage.db import expected_schema_revision


def test_expected_schema_revision_matches_current_migration_head() -> None:
    assert expected_schema_revision() == "f4a2d6e8c013"
