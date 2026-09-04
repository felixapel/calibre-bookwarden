from calibre_ai_auditor.providers.google_books import resolve_hd_cover_url


def test_resolve_hd_cover_url_with_thumbnail():
    thumb = "http://books.google.com/books/content?id=vol123&printsec=frontcover&img=1&zoom=1&edge=curl&source=gbs_api"
    hd = resolve_hd_cover_url(thumb, volume_id="vol123")
    assert hd is not None
    assert hd.startswith("https://")
    assert "edge=curl" not in hd
    assert "zoom=0" in hd


def test_resolve_hd_cover_url_with_zoom_5():
    thumb = "http://books.google.com/books/content?id=vol123&printsec=frontcover&img=1&zoom=5&edge=curl"
    hd = resolve_hd_cover_url(thumb)
    assert hd is not None
    assert hd.startswith("https://")
    assert "edge=curl" not in hd
    assert "zoom=0" in hd


def test_resolve_hd_cover_url_fallback_volume_id():
    hd = resolve_hd_cover_url(None, volume_id="abc999")
    assert hd == "https://books.google.com/books/publisher/content/images/frontcover/abc999?fife=w1000-h1500"


def test_resolve_hd_cover_url_empty():
    assert resolve_hd_cover_url(None, volume_id=None) is None
