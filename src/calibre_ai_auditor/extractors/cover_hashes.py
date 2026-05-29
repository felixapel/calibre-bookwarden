import logging
from pathlib import Path

import imagehash
from PIL import Image

logger = logging.getLogger(__name__)


def calculate_phash(image_path: Path) -> str | None:
    """
    Calculates the perceptual hash (phash) of an image.
    Useful for finding similar covers even if they have different resolutions/formats.
    """
    try:
        if not image_path.exists():
            return None
        with Image.open(image_path) as img:
            hash_obj = imagehash.phash(img)
            return str(hash_obj)
    except Exception as e:
        logger.warning(f"Failed to calculate phash for {image_path}: {e}")
        return None


def compare_phashes(hash1: str, hash2: str) -> int:
    """
    Compares two phashes and returns the Hamming distance.
    A distance of 0 means identical images.
    Typically, a distance <= 4 means the images are very likely the same.
    """
    try:
        h1 = imagehash.hex_to_hash(hash1)
        h2 = imagehash.hex_to_hash(hash2)
        return h1 - h2
    except Exception:
        return 100  # Maximum distance
