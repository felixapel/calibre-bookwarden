import zipfile
from pathlib import Path

from PIL import Image

from calibre_ai_auditor.covers.extractor import extract_cover_from_epub
from calibre_ai_auditor.covers.optimizer import CoverOptimizer


def test_cover_optimizer_bomb_detection(tmp_path: Path):
    optimizer = CoverOptimizer(tmp_path)

    # 1. Normal cover
    normal_cov = tmp_path / "normal_cover.jpg"
    im_normal = Image.new("RGB", (600, 900), color="blue")
    im_normal.save(normal_cov, "JPEG", quality=85)

    is_bomb, info = optimizer.is_decompression_bomb(normal_cov)
    assert is_bomb is False
    assert info["width"] == 600

    # 2. Oversized / decompression bomb candidate (e.g. 6000 x 6000 = 36 MP > 30 MP)
    bomb_cov = tmp_path / "bomb_cover.jpg"
    im_bomb = Image.new("RGB", (6000, 6000), color="red")
    im_bomb.save(bomb_cov, "JPEG", quality=30)

    is_bomb, info = optimizer.is_decompression_bomb(bomb_cov)
    assert is_bomb is True
    assert info["pixels"] == 36_000_000

    # 3. Optimize the bomb
    success, before, after = optimizer.optimize_cover(bomb_cov, backup=True)
    assert success is True
    assert (tmp_path / "bomb_cover.orig_bak").exists()

    with Image.open(bomb_cov) as optimized_im:
        ow, oh = optimized_im.size
        assert ow <= 1200
        assert oh <= 1800


def test_extract_cover_from_epub(tmp_path: Path):
    epub_file = tmp_path / "test_book.epub"
    target_cover = tmp_path / "extracted_cover.jpg"

    # Create dummy EPUB with an embedded cover
    im = Image.new("RGB", (800, 1200), color="green")
    img_bytes_path = tmp_path / "raw_cover.jpg"
    im.save(img_bytes_path, "JPEG")
    img_data = img_bytes_path.read_bytes()

    with zipfile.ZipFile(epub_file, "w") as z:
        z.writestr("mimetype", "application/epub+zip")
        z.writestr(
            "META-INF/container.xml",
            """<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>""",
        )
        z.writestr(
            "OEBPS/content.opf",
            """<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <manifest>
    <item id="cover-image" href="images/cover.jpg" media-type="image/jpeg" properties="cover-image"/>
  </manifest>
</package>""",
        )
        z.writestr("OEBPS/images/cover.jpg", img_data)

    extracted = extract_cover_from_epub(epub_file, target_cover)
    assert extracted is True
    assert target_cover.exists()
    assert target_cover.stat().st_size > 0
