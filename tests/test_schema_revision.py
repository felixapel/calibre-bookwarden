from calibre_ai_auditor.storage.db import expected_schema_revision


def test_expected_schema_revision_matches_current_migration_head() -> None:
    assert expected_schema_revision() == "a72c9d4e8f31"
