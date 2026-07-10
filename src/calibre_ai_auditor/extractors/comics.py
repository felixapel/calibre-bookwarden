import contextlib
import logging
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


def extract_comic_info_xml(file_path: Path) -> dict[str, Any] | None:
    """
    Attempts to extract and parse ComicInfo.xml from a .cbz (or zip-based .cbr) archive.
    """
    if not file_path.exists():
        return None

    suffix = file_path.suffix.lower()
    if suffix not in (".cbz", ".cbr"):
        return None

    # Try zipfile first (handles all .cbz and zip-based .cbr)
    try:
        if zipfile.is_zipfile(file_path):
            with zipfile.ZipFile(file_path, "r") as z:
                # Find ComicInfo.xml case-insensitively
                xml_filename = None
                for name in z.namelist():
                    if name.lower() == "comicinfo.xml":
                        xml_filename = name
                        break

                if xml_filename:
                    xml_data = z.read(xml_filename)
                    return parse_comic_info_xml_data(xml_data)

        # If it's a RAR file (.cbr), try to use rarfile library if available
        if suffix == ".cbr":
            try:
                import rarfile  # type: ignore[import-not-found]

                if rarfile.is_rarfile(str(file_path)):
                    with rarfile.RarFile(str(file_path), "r") as rf:
                        xml_filename = None
                        for name in rf.namelist():
                            if name.lower() == "comicinfo.xml":
                                xml_filename = name
                                break
                        if xml_filename:
                            xml_data = rf.read(xml_filename)
                            return parse_comic_info_xml_data(xml_data)
            except ImportError:
                logger.debug("rarfile library not installed; skipping RAR .cbr metadata extraction.")
            except Exception as e:
                logger.warning(f"Failed to read RAR archive {file_path}: {e}")

    except Exception as e:
        logger.error(f"Failed to extract ComicInfo.xml from {file_path}: {e}")

    return None


def parse_comic_info_xml_data(xml_data: bytes) -> dict[str, Any] | None:
    """
    Parses ComicInfo.xml data into a standard metadata dictionary.
    """
    try:
        root = ET.fromstring(xml_data)
        metadata = {}

        # Standard mappings
        # ComicInfo.xml elements mapping to calibre fields
        mappings = {
            "Title": "title",
            "Series": "series",
            "Number": "series_index",
            "Volume": "volume",
            "Chapter": "chapter",  # for comics
            "Writer": "authors",
            "Publisher": "publisher",
            "Year": "year",
            "Month": "month",
            "Genre": "tags",
        }

        for element_name, meta_key in mappings.items():
            elem = root.find(element_name)
            if elem is not None and elem.text:
                val: Any = elem.text.strip()
                if meta_key == "authors":
                    # Split comma-separated writers
                    val = [a.strip() for a in val.split(",") if a.strip()]
                elif meta_key == "tags":
                    # Split comma-separated genres/tags
                    val = [t.strip() for t in val.split(",") if t.strip()]
                elif meta_key == "series_index":
                    with contextlib.suppress(ValueError):
                        val = float(val)
                elif meta_key == "volume":
                    with contextlib.suppress(ValueError):
                        val = int(val)
                elif meta_key == "chapter":
                    with contextlib.suppress(ValueError):
                        val = float(val)  # decimal per convention
                metadata[meta_key] = val

        # Handle publish date if Year/Month exist
        if "year" in metadata:
            year = metadata.pop("year")
            month = metadata.pop("month", "01")
            metadata["published_date"] = f"{year}-{month.zfill(2)}-01"

        return metadata
    except Exception as e:
        logger.error(f"Error parsing ComicInfo.xml XML: {e}")
        return None
