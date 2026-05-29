import logging
import xml.etree.ElementTree as ET
from typing import Any

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.providers.base import BaseProvider
from calibre_ai_auditor.storage.models import Candidate, Metadata

logger = logging.getLogger(__name__)


class CalibreFetchProvider(BaseProvider):
    def __init__(self, cli: CalibreCLI):
        self.cli = cli

    @property
    def name(self) -> str:
        return "calibre_fetch"

    async def fetch_candidates(
        self,
        title: str | None = None,
        authors: list[str] | None = None,
        isbn: str | None = None,
    ) -> list[Candidate]:
        if not (title or authors or isbn):
            return []

        try:
            # fetch_metadata returns a string containing one or more OPF records
            opf_data = self.cli.fetch_metadata(title=title, authors=authors, isbn=isbn)
            if not opf_data:
                return []

            # Calibre fetch-ebook-metadata outputs OPFs separated by a horizontal line usually?
            # Or just concatenated. Actually, ET.fromstring might fail if there are multiple.
            # A common pattern is to split by '<?xml'
            records = opf_data.split("<?xml")
            candidates = []
            for i, rec in enumerate(records):
                if not rec.strip():
                    continue
                xml_str = "<?xml" + rec
                try:
                    cand = self._parse_single_opf(xml_str, f"calibre_fetch_{i}")
                    if cand:
                        candidates.append(cand)
                except Exception as e:
                    logger.debug(f"Failed to parse one OPF record: {e}")

            return candidates
        except Exception as e:
            logger.error(f"Calibre fetch failed: {e}")
            return []

    def _parse_single_opf(self, opf_str: str, record_id: str) -> Candidate | None:
        try:
            # Simple OPF parsing with ElementTree
            # OPF namespaces can be tricky
            root = ET.fromstring(opf_str)

            # Find metadata section
            metadata_node = None
            for child in root:
                if child.tag.endswith("metadata"):
                    metadata_node = child
                    break

            if metadata_node is None:
                return None

            ns = {"dc": "http://purl.org/dc/elements/1.1/", "opf": "http://www.idpf.org/2007/opf"}

            title = metadata_node.find(".//dc:title", ns)
            title_text = title.text if title is not None else None

            authors = []
            for author in metadata_node.findall(".//dc:creator", ns):
                if author.text:
                    authors.append(author.text)

            publisher = metadata_node.find(".//dc:publisher", ns)
            pub_text = publisher.text if publisher is not None else None

            date = metadata_node.find(".//dc:date", ns)
            date_text = date.text if date is not None else None

            language = metadata_node.find(".//dc:language", ns)
            lang_text = language.text if language is not None else None

            identifiers = {}
            for ident in metadata_node.findall(".//dc:identifier", ns):
                scheme = ident.get("{http://www.idpf.org/2007/opf}scheme")
                if scheme and ident.text:
                    identifiers[scheme.lower()] = ident.text

            metadata = Metadata(
                title=title_text,
                authors=authors,
                publisher=pub_text,
                published_date=date_text,
                language=lang_text,
                identifiers=identifiers,
            )

            return Candidate(candidate_id=record_id, provider=self.name, metadata=metadata)
        except Exception as e:
            logger.warning(f"Error parsing OPF: {e}")
            return None

    def normalize_metadata(self, _raw_data: Any) -> Metadata:
        return Metadata()
