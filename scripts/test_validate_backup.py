import base64
import io
import hashlib
import os
import sqlite3
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

from validate_backup import BackupValidationError, finalize_backup, validate_backup


SEED = bytes.fromhex("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
EXPECTED_PUBLIC_KEY = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
PKCS8_PREFIX = bytes.fromhex("302e020100300506032b657004220420")


def key_pem(seed=SEED):
    der = PKCS8_PREFIX + seed
    payload = base64.b64encode(der)
    return b"-----BEGIN PRIVATE KEY-----\n" + payload + b"\n-----END PRIVATE KEY-----\n"


def create_database(path, federation_enabled=0, federation_origin="", *, federation_columns=True):
    connection = sqlite3.connect(path)
    try:
        if federation_columns:
            connection.execute(
                "CREATE TABLE instance_settings (id INTEGER PRIMARY KEY, federation_origin TEXT NOT NULL, federation_enabled INTEGER NOT NULL)"
            )
            connection.execute(
                "INSERT INTO instance_settings VALUES (1, ?, ?)",
                (federation_origin, federation_enabled),
            )
        else:
            connection.execute("CREATE TABLE instance_settings (id INTEGER PRIMARY KEY)")
            connection.execute("INSERT INTO instance_settings VALUES (1)")
        connection.commit()
    finally:
        connection.close()
    path.chmod(0o600)


def create_archive(path, *, include_key=True, include_pending=False, key_data=None, legacy=False, old_schema=False, entries=None, federation_enabled=0, federation_origin=""):
    if entries is None:
        with tempfile.TemporaryDirectory(prefix="cookbook-backup-test-") as temporary:
            database_path = Path(temporary) / "cookbook.db"
            create_database(database_path, federation_enabled, federation_origin, federation_columns=not old_schema)
            with tarfile.open(path, mode="w:gz") as archive:
                database = tarfile.TarInfo("cookbook.db")
                database.mode = 0o600
                data = database_path.read_bytes()
                database.size = len(data)
                archive.addfile(database, io.BytesIO(data))
                media = tarfile.TarInfo("media/")
                media.type = tarfile.DIRTYPE
                media.mode = 0o700
                archive.addfile(media)
                if not legacy:
                    secrets = tarfile.TarInfo("secrets/")
                    secrets.type = tarfile.DIRTYPE
                    secrets.mode = 0o700
                    archive.addfile(secrets)
                    if include_key:
                        key = tarfile.TarInfo("secrets/federation-ed25519.pk8.pem")
                        key.mode = 0o600
                        data = key_data if key_data is not None else key_pem()
                        key.size = len(data)
                        archive.addfile(key, io.BytesIO(data))
                    if include_pending:
                        key = tarfile.TarInfo("secrets/federation-ed25519.pending.pk8.pem")
                        key.mode = 0o600
                        data = key_pem(bytes.fromhex("4c" * 32))
                        key.size = len(data)
                        archive.addfile(key, io.BytesIO(data))
        path.chmod(0o600)
        return

    with tarfile.open(path, mode="w:gz") as archive:
        for name, kind, value in entries:
            info = tarfile.TarInfo(name)
            info.mode = 0o700 if kind == "directory" else 0o600
            if kind == "directory":
                info.type = tarfile.DIRTYPE
                archive.addfile(info)
            elif kind == "symlink":
                info.type = tarfile.SYMTYPE
                info.linkname = value
                archive.addfile(info)
            else:
                data = value if isinstance(value, bytes) else value.encode()
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
    path.chmod(0o600)


class BackupValidationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="cookbook-backup-tests-")
        self.root = Path(self.temporary.name)
        self.paths = []

    def tearDown(self):
        self.temporary.cleanup()

    def archive(self, *, finalize=True, **kwargs):
        path = self.root / f"archive-{len(self.paths)}.tar.gz"
        create_archive(path, **kwargs)
        self.paths.append(path)
        if finalize:
            finalize_backup(path)
        return path

    def test_archive_preserves_active_and_pending_key_file_fingerprints(self):
        path = self.archive(include_pending=True)
        manifest = validate_backup(path)
        active = manifest["federation_key_files"]["active"]
        self.assertEqual(active["sha256"], hashlib.sha256(key_pem()).hexdigest())
        pending = manifest["federation_key_files"]["pending"]
        self.assertEqual(pending["sha256"], hashlib.sha256(key_pem(bytes.fromhex("4c" * 32))).hexdigest())
        self.assertEqual(
            manifest["database"],
            {"federation_enabled": False, "federation_origin": "", "state": "known"},
        )

    def test_restored_fixture_keeps_database_settings_and_exact_key_bytes(self):
        path = self.archive(
            include_pending=True,
            federation_enabled=1,
            federation_origin="https://instance.example",
        )
        manifest = validate_backup(path)
        restored = self.root / "restored"
        restored.mkdir()
        names = [
            "cookbook.db",
            "secrets/federation-ed25519.pk8.pem",
            "secrets/federation-ed25519.pending.pk8.pem",
        ]
        with tarfile.open(path, mode="r:gz") as archive:
            for name in names:
                destination = restored / name
                destination.parent.mkdir(exist_ok=True)
                destination.write_bytes(archive.extractfile(name).read())
        active = restored / "secrets/federation-ed25519.pk8.pem"
        pending = restored / "secrets/federation-ed25519.pending.pk8.pem"
        self.assertEqual(hashlib.sha256(active.read_bytes()).hexdigest(), manifest["federation_key_files"]["active"]["sha256"])
        self.assertEqual(hashlib.sha256(pending.read_bytes()).hexdigest(), manifest["federation_key_files"]["pending"]["sha256"])
        connection = sqlite3.connect((restored / "cookbook.db").as_uri() + "?mode=ro", uri=True)
        try:
            row = connection.execute("SELECT federation_origin, federation_enabled FROM instance_settings WHERE id=1").fetchone()
        finally:
            connection.close()
        self.assertEqual(row, ("https://instance.example", 1))

    def test_restored_fixture_retains_ed25519_signing_identity(self):
        go = shutil.which("go")
        if go is None:
            self.skipTest("Go toolchain is unavailable for signing continuity verification")
        path = self.archive(federation_enabled=1, federation_origin="https://instance.example")
        restored = self.root / "restored-signing"
        restored.mkdir()
        with tarfile.open(path, mode="r:gz") as archive:
            restored_key = restored / "federation-ed25519.pk8.pem"
            restored_key.write_bytes(archive.extractfile("secrets/federation-ed25519.pk8.pem").read())
        original_key = self.root / "original-federation-key.pem"
        original_key.write_bytes(key_pem())
        helper = self.root / "verify_signing.go"
        helper.write_text(
            "package main\n"
            "import (\"crypto/ed25519\"; \"crypto/sha256\"; \"crypto/x509\"; \"encoding/hex\"; \"encoding/pem\"; \"fmt\"; \"os\")\n"
            "func main() { for _, path := range os.Args[1:] { data, err := os.ReadFile(path); if err != nil { panic(err) }; block, _ := pem.Decode(data); if block == nil { panic(\"PEM decode failed\") }; parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); if err != nil { panic(err) }; key, ok := parsed.(ed25519.PrivateKey); if !ok { panic(\"not Ed25519\") }; public := key.Public().(ed25519.PublicKey); digest := sha256.Sum256(public); message := []byte(\"Cookbook backup continuity fixture\"); signature := ed25519.Sign(key, message); if !ed25519.Verify(public, message, signature) { panic(\"signature verification failed\") }; fmt.Printf(\"cookbook-ed25519-%x %s\\n\", digest[:16], hex.EncodeToString(signature)) } }\n"
        )
        environment = os.environ.copy()
        environment["GO111MODULE"] = "off"
        environment["GOCACHE"] = str(self.root / "go-cache")
        result = subprocess.run(
            [go, "run", str(helper), str(original_key), str(restored_key)],
            cwd=self.root,
            env=environment,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines()
        self.assertEqual(len(lines), 2)
        self.assertEqual(lines[0], lines[1])

    def test_enabled_federation_requires_an_origin_and_active_key(self):
        with self.assertRaisesRegex(BackupValidationError, "origin"):
            self.archive(federation_enabled=1, federation_origin="")
        with self.assertRaisesRegex(BackupValidationError, "active federation signing key"):
            self.archive(include_key=False)

    def test_rejects_malformed_key_pem_without_attempting_crypto_validation(self):
        with self.assertRaisesRegex(BackupValidationError, "PRIVATE KEY PEM"):
            self.archive(key_data=b"not a private key")

    def test_detects_key_bytes_changed_after_manifest_creation(self):
        path = self.archive()
        replacement = path.with_suffix(".replacement.tar.gz")
        with tarfile.open(path, mode="r:gz") as source, tarfile.open(replacement, mode="w:gz") as output:
            for member in source.getmembers():
                if member.name == "secrets/federation-ed25519.pk8.pem":
                    continue
                data = source.extractfile(member).read() if member.isfile() else b""
                info = tarfile.TarInfo(member.name)
                info.mode = member.mode
                info.type = member.type
                info.size = len(data) if member.isfile() else 0
                output.addfile(info, io.BytesIO(data) if member.isfile() else None)
            info = tarfile.TarInfo("secrets/federation-ed25519.pk8.pem")
            info.mode = 0o600
            data = key_pem(bytes.fromhex("4c" * 32))
            info.size = len(data)
            output.addfile(info, io.BytesIO(data))
        replacement.chmod(0o600)
        self.paths.append(replacement)
        with self.assertRaisesRegex(BackupValidationError, "checksum mismatch"):
            validate_backup(replacement)

    def test_detects_database_replaced_without_rebuilding_identity_manifest(self):
        path = self.archive()
        replacement_db = self.root / "changed.db"
        create_database(replacement_db, federation_enabled=1, federation_origin="https://changed.example")
        replacement = path.with_suffix(".changed.tar.gz")
        with tarfile.open(path, mode="r:gz") as source, tarfile.open(replacement, mode="w:gz") as output:
            for member in source.getmembers():
                data = source.extractfile(member).read() if member.isfile() else b""
                if member.name == "cookbook.db":
                    data = replacement_db.read_bytes()
                info = tarfile.TarInfo(member.name)
                info.mode = member.mode
                info.type = member.type
                info.size = len(data) if member.isfile() else 0
                output.addfile(info, io.BytesIO(data) if member.isfile() else None)
        replacement.chmod(0o600)
        self.paths.append(replacement)
        with self.assertRaisesRegex(BackupValidationError, "checksum mismatch: cookbook.db"):
            validate_backup(replacement)

    def test_refuses_legacy_archive_without_identity_manifest(self):
        path = self.archive(finalize=False, legacy=True)
        with self.assertRaisesRegex(BackupValidationError, "explicit key-loss acknowledgement"):
            validate_backup(path)
        manifest = validate_backup(path, allow_legacy=True)
        self.assertEqual(manifest["federation_identity_status"], "unavailable")

    def test_legacy_database_without_federation_columns_is_marked_unknown(self):
        manifest = validate_backup(self.archive(finalize=False, legacy=True, old_schema=True), allow_legacy=True)
        self.assertEqual(manifest["database"]["state"], "unknown")

    def test_rejects_traversal_symlink_duplicate_and_invalid_media(self):
        valid = [
            ("cookbook.db", "file", "not a database"),
            ("media/", "directory", ""),
            ("secrets/", "directory", ""),
            ("secrets/federation-ed25519.pk8.pem", "file", key_pem()),
        ]
        invalids = [
            valid + [("../escape", "file", "x")],
            valid + [("media/abcdef0123456789abcdef0123456789.png", "symlink", "/etc/passwd")],
            valid + [("cookbook.db", "file", "duplicate")],
            valid + [("media/private.txt", "file", "x")],
        ]
        for entries in invalids:
            with self.subTest(entries=entries[-1][0]):
                with self.assertRaises(BackupValidationError):
                    validate_backup(self.archive(finalize=False, entries=entries))

    def test_rejects_media_larger_than_the_upload_limit(self):
        entries = [
            ("cookbook.db", "file", "not a database"),
            ("media/", "directory", ""),
            ("secrets/", "directory", ""),
            ("secrets/federation-ed25519.pk8.pem", "file", key_pem()),
            ("media/abcdef0123456789abcdef0123456789.png", "file", b"x" * ((5 << 20) + 1)),
        ]
        with self.assertRaisesRegex(BackupValidationError, "upload size limit"):
            validate_backup(self.archive(finalize=False, entries=entries))

    def test_rejects_key_with_permissive_archive_mode(self):
        path = self.archive(finalize=False)
        rewritten = path.with_suffix(".permissive.tar.gz")
        with tarfile.open(path, mode="r:gz") as source, tarfile.open(rewritten, mode="w:gz") as output:
            for member in source.getmembers():
                data = source.extractfile(member).read() if member.isfile() else b""
                info = tarfile.TarInfo(member.name)
                info.mode = 0o644 if member.name.startswith("secrets/") and member.isfile() else member.mode
                info.type = member.type
                info.size = len(data) if member.isfile() else 0
                output.addfile(info, io.BytesIO(data) if member.isfile() else None)
        rewritten.chmod(0o600)
        self.paths.append(rewritten)
        with self.assertRaisesRegex(BackupValidationError, "mode 0600"):
            validate_backup(rewritten)


if __name__ == "__main__":
    unittest.main()
