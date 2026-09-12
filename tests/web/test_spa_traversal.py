"""Regression: SPA static containment must block traversal escapes."""

from calibre_ai_auditor.web.app import is_path_within_static


def test_traversal_blocked(tmp_path):
    static = tmp_path / "static"
    static.mkdir()
    (static / "index.html").write_text("ok")
    outside = tmp_path / "secret.txt"
    outside.write_text("secret")

    assert is_path_within_static(str(static), str(static / "index.html"))
    assert not is_path_within_static(str(static), str(outside))
    assert not is_path_within_static(str(static), str(static / ".." / "secret.txt"))
    assert not is_path_within_static(str(static), str(static / ".." / ".." / "etc" / "passwd"))
