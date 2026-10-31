#!/usr/bin/env python3
"""Validate the narrow tar format produced by backup-compose.sh."""

import re
import tarfile
from pathlib import Path

_IMAGE_NAME = re.compile(r"[0-9a-fA-F]{32}\.(?:jpg|png|webp)\Z")
_DATABASE_FILES = {"cookbook.db", "cookbook.db-wal", "cookbook.db-journal"}
_MAX_MEMBERS = 100_000
_MAX_IMAGE_BYTES = 5 << 20


class BackupValidationError(ValueError):
    """Raised when a backup contains paths or file types the app never writes."""


def validate_backup(path: Path) -> None:
    seen: set[str] = set()
    has_database = False
    has_media_directory = False

    try:
        with tarfile.open(path, mode="r:gz") as archive:
            members = archive.getmembers()
            if len(members) > _MAX_MEMBERS:
                raise BackupValidationError("backup contains too many entries")

            for member in members:
                raw_name = member.name
                if not raw_name or "\\" in raw_name or raw_name.startswith("/"):
                    raise BackupValidationError("backup contains an invalid path")
                name = raw_name[:-1] if raw_name.endswith("/") else raw_name
                parts = name.split("/")
                if not name or any(part in ("", ".", "..") for part in parts):
                    raise BackupValidationError("backup contains an unsafe path")
                if name in seen:
                    raise BackupValidationError("backup contains duplicate paths")
                seen.add(name)

                if name == "media" and member.isdir():
                    has_media_directory = True
                    continue
                if name in _DATABASE_FILES:
                    if not member.isfile():
                        raise BackupValidationError("database entries must be regular files")
                    if name == "cookbook.db" and member.size == 0:
                        raise BackupValidationError("database file is empty")
                    has_database |= name == "cookbook.db"
                    continue
                if len(parts) == 2 and parts[0] == "media" and _IMAGE_NAME.fullmatch(parts[1]):
                    if not member.isfile():
                        raise BackupValidationError("media entries must be regular files")
                    if member.size > _MAX_IMAGE_BYTES:
                        raise BackupValidationError("media entry exceeds the upload size limit")
                    continue
                raise BackupValidationError(f"backup contains an unexpected entry: {raw_name}")
    except (OSError, tarfile.TarError) as exc:
        raise BackupValidationError("backup is not a readable gzip tar archive") from exc

    if not has_database or not has_media_directory:
        raise BackupValidationError("backup must contain cookbook.db and the media directory")


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    args = parser.parse_args()
    try:
        validate_backup(args.archive)
    except BackupValidationError as exc:
        parser.error(str(exc))
