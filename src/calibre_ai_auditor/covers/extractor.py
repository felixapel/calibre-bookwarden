"""Forensic native cover extractor from EPUB packages.

Directly inspects the EPUB ZIP container, resolves the OPF manifest to find
official cover images (calibre_cover.jpg, cover.jpg, or largest embedded art),
and extracts it cleanly without external network calls.
"""

from __future__ import annotations

import logging
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path

logger = logging.getLogger(__name__)


def extract_cover_from_epub(epub_path: Path | str, target_cover_path: Path | str) -> bool:
    """
    Extracts the highest-quality authentic cover image from an EPUB file.
    Returns True if an image was successfully extracted.
    """
    ep = Path(epub_path)
    dest = Path(target_cover_path)

    if not ep.exists():
        logger.warning(f"EPUB does not exist: {ep}")
        return False

    try:
        with zipfile.ZipFile(ep, "r") as z:
            names = z.namelist()
            image_names = [
                n for n in names if any(n.lower().endswith(ext) for ext in [".jpg", ".jpeg", ".png", ".webp"])
            ]
            if not image_names:
                return False

            # Strategy 1: Explicit 'calibre_cover'
            calibre_covers = [n for n in image_names if "calibre_cover" in n.lower()]
            if calibre_covers:
                dest.parent.mkdir(parents=True, exist_ok=True)
                with open(dest, "wb") as f:
                    f.write(z.read(calibre_covers[0]))
                return True

            # Strategy 2: Parse container.xml & OPF for cover-image item or cover meta
            cover_href = _find_cover_href_from_opf(z)
            if cover_href and cover_href in names:
                dest.parent.mkdir(parents=True, exist_ok=True)
                with open(dest, "wb") as f:
                    f.write(z.read(cover_href))
                return True

            # Strategy 3: Search for file named 'cover' (e.g. cover.jpg, OEBPS/cover.jpg)
            named_covers = [n for n in image_names if Path(n).stem.lower() in ("cover", "front_cover", "portada")]
            if named_covers:
                # Pick largest if multiple
                best = max(named_covers, key=lambda x: len(z.read(x)))
                dest.parent.mkdir(parents=True, exist_ok=True)
                with open(dest, "wb") as f:
                    f.write(z.read(best))
                return True

            # Strategy 4: Fallback to the largest image in the first 5 image entries
            if image_names:
                largest = max(image_names[:5], key=lambda x: len(z.read(x)))
                if len(z.read(largest)) > 15_000:
                    dest.parent.mkdir(parents=True, exist_ok=True)
                    with open(dest, "wb") as f:
                        f.write(z.read(largest))
                    return True

    except Exception as exc:
        logger.error(f"Failed to extract cover from EPUB {ep}: {exc}")
        return False

    return False


def _find_cover_href_from_opf(z: zipfile.ZipFile) -> str | None:
    """Parses OPF package to find the canonical cover image path."""
    try:
        # Find OPF path from META-INF/container.xml
        container_data = z.read("META-INF/container.xml")
        root = ET.fromstring(container_data)
        ns = {"ns": "urn:oasis:names:tc:opendocument:xmlns:container"}
        rootfile = root.find(".//ns:rootfile", ns)
        if rootfile is None:
            return None
        opf_path = rootfile.get("full-path")
        if not opf_path or opf_path not in z.namelist():
            return None

        opf_dir = str(Path(opf_path).parent)
        if opf_dir == ".":
            opf_dir = ""

        opf_data = z.read(opf_path)
        opf_root = ET.fromstring(opf_data)

        # Look for <meta name="cover" content="item_id"/>
        cover_id = None
        for meta in opf_root.findall(".//{*}meta"):
            if meta.get("name") == "cover":
                cover_id = meta.get("content")
                break

        # Look in manifest for cover_id or properties="cover-image"
        for item in opf_root.findall(".//{*}item"):
            item_id = item.get("id")
            props = item.get("properties", "")
            if (cover_id and item_id == cover_id) or "cover-image" in props:
                href = item.get("href")
                if href:
                    full_href = f"{opf_dir}/{href}".lstrip("/") if opf_dir else href
                    return full_href

    except Exception:
        pass
    return None
