#!/usr/bin/env python3
"""Finalize and validate versioned Cookbook full-instance backup archives."""

import base64
import hashlib
import io
import json
import os
import re
import shutil
import sqlite3
import tarfile
import tempfile
from pathlib import Path

_IMAGE_NAME = re.compile(r"[0-9a-fA-F]{32}\.(?:jpg|png|webp)\Z")
_DATABASE_FILES = {"cookbook.db", "cookbook.db-wal", "cookbook.db-journal"}
_KEY_FILES = {
    "secrets/federation-ed25519.pk8.pem": "active",
    "secrets/federation-ed25519.pending.pk8.pem": "pending",
}
_MANIFEST = "backup-manifest.json"
_CHECKSUMS = "backup-manifest.sha256"
_FORMAT_VERSION = 2
_MAX_MEMBERS = 100_000
_MAX_IMAGE_BYTES = 5 << 20
_MAX_ARCHIVE_BYTES = 50 << 30
_MAX_UNCOMPRESSED_BYTES = 50 << 30


class BackupValidationError(ValueError):
    """Raised when an archive is incomplete, unsafe, or internally inconsistent."""


def _key_file_fingerprint(data: bytes) -> str:
    begin = b"-----BEGIN PRIVATE KEY-----\n"
    end = b"\n-----END PRIVATE KEY-----\n"
    if not data.startswith(begin) or not data.endswith(end):
        raise BackupValidationError("federation key file is not a bounded PRIVATE KEY PEM")
    payload = b"".join(data[len(begin) : -len(end)].split())
    try:
        decoded = base64.b64decode(payload, validate=True)
    except (ValueError, base64.binascii.Error) as exc:
        raise BackupValidationError("federation key PEM body is not valid base64") from exc
    if not decoded:
        raise BackupValidationError("federation key PEM body is empty")
    return hashlib.sha256(data).hexdigest()


def _valid_path(raw_name: str) -> str:
    if not raw_name or "\\" in raw_name or raw_name.startswith("/"):
        raise BackupValidationError("backup contains an invalid path")
    name = raw_name[:-1] if raw_name.endswith("/") else raw_name
    parts = name.split("/")
    if not name or any(part in ("", ".", "..") for part in parts):
        raise BackupValidationError("backup contains an unsafe path")
    return name


def _read_member(archive: tarfile.TarFile, member: tarfile.TarInfo, limit: int = _MAX_ARCHIVE_BYTES) -> bytes:
    if member.size > limit:
        raise BackupValidationError(f"backup entry exceeds the size limit: {member.name}")
    stream = archive.extractfile(member)
    if stream is None:
        raise BackupValidationError(f"backup entry cannot be read: {member.name}")
    data = stream.read(member.size + 1)
    if len(data) != member.size:
        raise BackupValidationError(f"backup entry size is inconsistent: {member.name}")
    return data


def _sha256_member(archive: tarfile.TarFile, member: tarfile.TarInfo) -> str:
    if member.size > _MAX_UNCOMPRESSED_BYTES:
        raise BackupValidationError(f"backup entry exceeds the size limit: {member.name}")
    stream = archive.extractfile(member)
    if stream is None:
        raise BackupValidationError(f"backup entry cannot be read: {member.name}")
    digest = hashlib.sha256()
    total = 0
    while True:
        chunk = stream.read(1 << 20)
        if not chunk:
            break
        total += len(chunk)
        if total > member.size:
            raise BackupValidationError(f"backup entry size is inconsistent: {member.name}")
        digest.update(chunk)
    if total != member.size:
        raise BackupValidationError(f"backup entry size is inconsistent: {member.name}")
    return digest.hexdigest()


def _inspect_members(archive: tarfile.TarFile, *, allow_unfinalized: bool = False, allow_legacy: bool = False):
    members = archive.getmembers()
    if len(members) > _MAX_MEMBERS:
        raise BackupValidationError("backup contains too many entries")
    by_name = {}
    total_size = 0
    has_media_directory = False
    has_database = False
    has_secrets_directory = False
    for member in members:
        name = _valid_path(member.name)
        if name in by_name:
            raise BackupValidationError("backup contains duplicate paths")
        by_name[name] = member
        total_size += member.size
        if total_size > _MAX_UNCOMPRESSED_BYTES:
            raise BackupValidationError("backup exceeds the uncompressed size limit")

        if name == "media" and member.isdir():
            has_media_directory = True
            continue
        if name == "secrets" and member.isdir():
            if member.mode & 0o077 or member.mode & 0o700 != 0o700:
                raise BackupValidationError("secrets directory must have owner-only access and owner traversal")
            has_secrets_directory = True
            continue
        if name in (_MANIFEST, _CHECKSUMS):
            if allow_unfinalized:
                raise BackupValidationError("raw backup unexpectedly contains a manifest")
            if not member.isfile() or member.mode & 0o077 or member.mode & 0o111:
                raise BackupValidationError("backup manifest files must be owner-only regular files")
            continue
        if name in _DATABASE_FILES:
            if not member.isfile():
                raise BackupValidationError("database entries must be regular files")
            if member.mode & 0o077 or member.mode & 0o111:
                raise BackupValidationError("database entries must be owner-only regular files")
            if name == "cookbook.db" and member.size == 0:
                raise BackupValidationError("database file is empty")
            has_database |= name == "cookbook.db"
            continue
        if name in _KEY_FILES:
            if not member.isfile() or member.mode != 0o600:
                raise BackupValidationError("federation private keys must be regular files with mode 0600")
            continue
        if len(name.split("/")) == 2 and name.startswith("media/") and _IMAGE_NAME.fullmatch(name.split("/", 1)[1]):
            if not member.isfile():
                raise BackupValidationError("media entries must be regular files")
            if member.mode & 0o077 or member.mode & 0o111:
                raise BackupValidationError("media entries must be owner-only regular files")
            if member.size > _MAX_IMAGE_BYTES:
                raise BackupValidationError("media entry exceeds the upload size limit")
            continue
        raise BackupValidationError(f"backup contains an unexpected entry: {member.name}")

    legacy = not has_secrets_directory and not any(name in by_name for name in _KEY_FILES)
    if not has_database or not has_media_directory:
        raise BackupValidationError("backup must contain the database and media directory")
    if legacy:
        if allow_unfinalized:
            raise BackupValidationError("new backup is missing the federation secrets directory")
        if not allow_legacy:
            raise BackupValidationError(
                "legacy backup has no federation identity manifest; continuity cannot be established and restore is refused without explicit key-loss acknowledgement"
            )
        if _MANIFEST in by_name or _CHECKSUMS in by_name:
            raise BackupValidationError("legacy backup unexpectedly contains a version 2 manifest")
        return by_name
    if not has_secrets_directory:
        raise BackupValidationError("federation private keys must be inside the secrets directory")
    if "secrets/federation-ed25519.pk8.pem" not in by_name:
        raise BackupValidationError("backup is missing the active federation signing key")
    if allow_unfinalized and (_MANIFEST in by_name or _CHECKSUMS in by_name):
        raise BackupValidationError("raw backup unexpectedly contains a manifest")
    if not allow_unfinalized and (_MANIFEST not in by_name or _CHECKSUMS not in by_name):
        raise BackupValidationError(
            "legacy backup has no federation identity manifest; identity continuity cannot be established and restore is refused"
        )
    return by_name


def _database_identity(archive: tarfile.TarFile, by_name) -> dict:
    with tempfile.TemporaryDirectory(prefix="cookbook-backup-validate-") as temporary:
        database_path = Path(temporary) / "cookbook.db"
        for name in _DATABASE_FILES:
            member = by_name.get(name)
            if member is not None:
                stream = archive.extractfile(member)
                if stream is None:
                    raise BackupValidationError(f"backup entry cannot be read: {name}")
                with (Path(temporary) / name).open("wb") as output:
                    shutil.copyfileobj(stream, output, length=1 << 20)
        try:
            connection = sqlite3.connect(database_path.as_uri() + "?mode=ro", uri=True)
            try:
                columns = {
                    row[1] for row in connection.execute("PRAGMA table_info(instance_settings)").fetchall()
                }
                if not {"federation_origin", "federation_enabled"}.issubset(columns):
                    return {"federation_enabled": None, "federation_origin": None, "state": "unknown"}
                row = connection.execute(
                    "SELECT federation_origin, federation_enabled FROM instance_settings WHERE id=1"
                ).fetchone()
            finally:
                connection.close()
        except sqlite3.Error as exc:
            raise BackupValidationError("backup database cannot be read for federation identity checks") from exc
    if row is None or row[1] not in (0, 1):
        raise BackupValidationError("backup database has invalid federation settings")
    origin = row[0]
    if not isinstance(origin, str) or (row[1] == 1 and not origin):
        raise BackupValidationError("backup database federation origin is inconsistent")
    return {"federation_enabled": bool(row[1]), "federation_origin": origin, "state": "known"}


def _describe_archive(archive: tarfile.TarFile, by_name) -> tuple[dict, bytes]:
    database_identity = _database_identity(archive, by_name)
    identities = {}
    checksum_lines = []
    for name, member in sorted(by_name.items()):
        if name in (_MANIFEST, _CHECKSUMS):
            continue
        if not member.isfile():
            continue
        checksum = _sha256_member(archive, member)
        checksum_lines.append(f"{checksum}  {name}\n")
        if name in _KEY_FILES:
            if _key_file_fingerprint(_read_member(archive, member, limit=4096)) != checksum:
                raise BackupValidationError("federation key fingerprint is inconsistent")
            identities[_KEY_FILES[name]] = {"path": name, "sha256": checksum}
    if "active" not in identities:
        raise BackupValidationError("backup is missing the active federation signing key")
    manifest = {
        "format": "cookbook-full-backup",
        "format_version": _FORMAT_VERSION,
        "database": database_identity,
        "federation_key_files": {
            "active": identities["active"],
            "pending": identities.get("pending"),
        },
    }
    return manifest, ("".join(checksum_lines)).encode()


def _read_and_validate_manifest(archive: tarfile.TarFile, by_name) -> None:
    try:
        manifest = json.loads(_read_member(archive, by_name[_MANIFEST], limit=64 << 10))
        checksums = _read_member(archive, by_name[_CHECKSUMS], limit=16 << 20).decode("ascii")
    except (UnicodeDecodeError, json.JSONDecodeError, KeyError) as exc:
        raise BackupValidationError("backup manifest is invalid") from exc
    if not isinstance(manifest, dict):
        raise BackupValidationError("backup manifest must be a JSON object")
    if manifest.get("format") != "cookbook-full-backup" or manifest.get("format_version") != _FORMAT_VERSION:
        raise BackupValidationError("unsupported backup format version")

    expected_names = {
        name for name, member in by_name.items() if member.isfile() and name not in (_MANIFEST, _CHECKSUMS)
    }
    actual_hashes = {}
    for line in checksums.splitlines():
        parts = line.split("  ", 1)
        if len(parts) != 2 or not re.fullmatch(r"[0-9a-f]{64}", parts[0]):
            raise BackupValidationError("backup checksum manifest is malformed")
        name = _valid_path(parts[1])
        if name in actual_hashes:
            raise BackupValidationError("backup checksum manifest contains duplicate paths")
        actual_hashes[name] = parts[0]
    if set(actual_hashes) != expected_names:
        raise BackupValidationError("backup checksum manifest does not match archive contents")
    for name, expected in actual_hashes.items():
        if _sha256_member(archive, by_name[name]) != expected:
            raise BackupValidationError(f"backup checksum mismatch: {name}")

    actual, _ = _describe_archive(archive, {name: member for name, member in by_name.items() if name not in (_MANIFEST, _CHECKSUMS)})
    if manifest != actual:
        raise BackupValidationError("backup manifest does not match its database and federation keys")


def finalize_backup(path: Path) -> None:
    """Add identity/checksum manifests to the raw archive written by backup-compose.sh."""
    path = Path(path)
    if path.stat().st_size > _MAX_ARCHIVE_BYTES:
        raise BackupValidationError("backup archive exceeds the size limit")
    try:
        with tarfile.open(path, mode="r:gz") as archive:
            by_name = _inspect_members(archive, allow_unfinalized=True)
            manifest, checksums_bytes = _describe_archive(archive, by_name)
            manifest_bytes = (json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n").encode()
    except (OSError, tarfile.TarError) as exc:
        raise BackupValidationError("backup is not a readable gzip tar archive") from exc

    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    os.close(descriptor)
    temporary_path = Path(temporary_name)
    try:
        with tarfile.open(path, mode="r:gz") as source, tarfile.open(temporary_path, mode="w:gz") as output:
            for original in source.getmembers():
                info = tarfile.TarInfo(original.name)
                info.type = original.type
                info.mode = original.mode
                info.size = original.size if original.isfile() else 0
                info.mtime = 0
                info.uid = info.gid = 0
                info.uname = info.gname = ""
                stream = source.extractfile(original) if original.isfile() else None
                output.addfile(info, stream)
            for name, data in ((_MANIFEST, manifest_bytes), (_CHECKSUMS, checksums_bytes)):
                info = tarfile.TarInfo(name)
                info.mode = 0o600
                info.size = len(data)
                info.mtime = 0
                output.addfile(info, io.BytesIO(data))
        temporary_path.chmod(0o600)
        os.replace(temporary_path, path)
        validate_backup(path)
    finally:
        temporary_path.unlink(missing_ok=True)


def validate_backup(path: Path, *, allow_legacy: bool = False) -> dict:
    path = Path(path)
    if path.stat().st_size > _MAX_ARCHIVE_BYTES:
        raise BackupValidationError("backup archive exceeds the size limit")
    try:
        with tarfile.open(path, mode="r:gz") as archive:
            by_name = _inspect_members(archive, allow_legacy=allow_legacy)
            if _MANIFEST not in by_name:
                database = _database_identity(archive, by_name)
                return {"format_version": 1, "federation_identity_status": "unavailable", "database": database}
            _read_and_validate_manifest(archive, by_name)
            return json.loads(_read_member(archive, by_name[_MANIFEST], limit=64 << 10))
    except (OSError, tarfile.TarError) as exc:
        raise BackupValidationError("backup is not a readable gzip tar archive") from exc


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("--finalize", action="store_true", help="finalize a raw archive written by the backup helper")
    parser.add_argument("--allow-legacy", action="store_true", help="inspect an old DB/media archive and warn that it has no bundled key")
    parser.add_argument("--print-format", action="store_true", help="print 2 or legacy after successful validation")
    args = parser.parse_args()
    try:
        if args.finalize:
            finalize_backup(args.archive)
        else:
            result = validate_backup(args.archive, allow_legacy=args.allow_legacy)
            if result["format_version"] == 1:
                print("WARNING: this legacy archive does not contain federation signing keys; key continuity cannot be established.", file=__import__("sys").stderr)
                if not args.allow_legacy:
                    raise BackupValidationError("legacy restore requires explicit federation key-loss acknowledgement")
            if args.print_format:
                print("legacy" if result["format_version"] == 1 else "2")
    except (BackupValidationError, OSError) as exc:
        parser.error(str(exc))
