import base64
import io
import os
import sqlite3
import sys
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from validate_backup import finalize_backup


ROOT = Path(__file__).resolve().parents[1]


def create_valid_archive(path):
    seed = bytes.fromhex("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
    key = b"-----BEGIN PRIVATE KEY-----\n" + base64.b64encode(bytes.fromhex("302e020100300506032b657004220420") + seed) + b"\n-----END PRIVATE KEY-----\n"
    with tempfile.TemporaryDirectory(prefix="cookbook-compose-fixture-") as temporary:
        database_path = Path(temporary) / "cookbook.db"
        with sqlite3.connect(database_path) as connection:
            connection.execute("CREATE TABLE instance_settings (id INTEGER PRIMARY KEY, federation_origin TEXT NOT NULL, federation_enabled INTEGER NOT NULL)")
            connection.execute("INSERT INTO instance_settings VALUES (1, '', 0)")
        with tarfile.open(path, mode="w:gz") as archive:
            database = tarfile.TarInfo("cookbook.db")
            database.mode = 0o600
            payload = database_path.read_bytes()
            database.size = len(payload)
            archive.addfile(database, io.BytesIO(payload))
            for name in ("media/", "secrets/"):
                directory = tarfile.TarInfo(name)
                directory.type = tarfile.DIRTYPE
                directory.mode = 0o700
                archive.addfile(directory)
            key_info = tarfile.TarInfo("secrets/federation-ed25519.pk8.pem")
            key_info.mode = 0o600
            key_info.size = len(key)
            archive.addfile(key_info, io.BytesIO(key))
    path.chmod(0o600)
    finalize_backup(path)


def create_raw_archive(source_path, raw_path, *, legacy=False):
    allowed = {"cookbook.db", "media", "media/"} if legacy else None
    with tarfile.open(source_path, mode="r:gz") as source, tarfile.open(raw_path, mode="w:gz") as target:
        for member in source.getmembers():
            if member.name in ("backup-manifest.json", "backup-manifest.sha256"):
                continue
            if allowed is not None and member.name not in allowed:
                continue
            data = source.extractfile(member).read() if member.isfile() else b""
            info = tarfile.TarInfo(member.name)
            info.mode = member.mode
            info.type = member.type
            info.size = len(data) if member.isfile() else 0
            target.addfile(info, io.BytesIO(data) if member.isfile() else None)
    raw_path.chmod(0o600)


class ComposeBackupScriptTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.log = self.root / "docker.log"
        self.run_count = self.root / "run-count"
        self.backup_dir = self.root / "backups"
        self.archive = self.root / "source.tar.gz"
        self.raw_archive = self.root / "raw.tar.gz"
        self._create_fake_docker()
        create_valid_archive(self.archive)
        create_raw_archive(self.archive, self.raw_archive)

    def tearDown(self):
        self.temporary.cleanup()

    def _create_fake_docker(self):
        docker = self.bin_dir / "docker"
        docker.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            "[ \"$1\" = compose ] || exit 90\n"
            "shift\n"
            "command=$1\n"
            "shift\n"
            "case \"$command\" in\n"
            "  ps) echo ps >> \"$MOCK_LOG\"; [ \"${MOCK_RUNNING:-yes}\" = yes ] && echo cookbook ;;\n"
            "  stop) echo stop >> \"$MOCK_LOG\" ;;\n"
            "  start) echo start >> \"$MOCK_LOG\" ;;\n"
            "  run)\n"
            "    echo run >> \"$MOCK_LOG\"\n"
            "    count=$(cat \"$MOCK_RUN_COUNT\" 2>/dev/null || echo 0)\n"
            "    count=$((count + 1))\n"
            "    echo \"$count\" > \"$MOCK_RUN_COUNT\"\n"
            "    [ \"${MOCK_FAIL_RUN_AT:-0}\" != \"$count\" ] || exit 42\n"
            "    if [ \"$count\" = 1 ] && [ -n \"${MOCK_ARCHIVE:-}\" ]; then\n"
            "      volume=\n"
            "      while [ $# -gt 0 ]; do\n"
            "        if [ \"$1\" = --volume ]; then volume=${2%%:*}; shift 2; continue; fi\n"
            "        shift\n"
            "      done\n"
            "      for target in \"$volume\"/.cookbook-backup.*; do [ ! -e \"$target\" ] || cp \"$MOCK_ARCHIVE\" \"$target\"; done\n"
            "    fi\n"
            "    ;;\n"
            "  *) exit 91 ;;\n"
            "esac\n"
        )
        docker.chmod(0o755)

    def _run(self, script, *args, fail_run_at=0, running="yes"):
        env = os.environ.copy()
        env.update(
            {
                "PATH": f"{self.bin_dir}:{env['PATH']}",
                "MOCK_LOG": str(self.log),
                "MOCK_RUN_COUNT": str(self.run_count),
                "MOCK_FAIL_RUN_AT": str(fail_run_at),
                "MOCK_RUNNING": running,
                "COOKBOOK_BACKUP_DIR": str(self.backup_dir),
                "MOCK_ARCHIVE": str(self.raw_archive),
                "COOKBOOK_COMPOSE_ENGINE": "docker",
            }
        )
        return subprocess.run([str(ROOT / "scripts" / script), *map(str, args)], cwd=ROOT, env=env, text=True, capture_output=True)

    def _events(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def test_backup_restarts_only_when_service_was_running(self):
        backup = self._run("backup-compose.sh", self.backup_dir)
        self.assertEqual(backup.returncode, 0, backup.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run", "start"])

        self.log.unlink()
        self.run_count.unlink()
        backup = self._run("backup-compose.sh", self.backup_dir, running="no")
        self.assertEqual(backup.returncode, 0, backup.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run"])

    def test_backup_restarts_service_after_archive_failure(self):
        result = self._run("backup-compose.sh", self.backup_dir, fail_run_at=1)
        self.assertEqual(result.returncode, 42)
        self.assertEqual(self._events(), ["ps", "stop", "run", "start"])

    def test_backup_refuses_a_shared_writable_directory(self):
        self.backup_dir.mkdir(mode=0o770)
        self.backup_dir.chmod(0o770)
        result = self._run("backup-compose.sh", self.backup_dir)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not be accessible by group or others", result.stderr)
        self.assertEqual(self._events(), [])

    def test_backup_refuses_a_shared_readable_directory(self):
        self.backup_dir.mkdir(mode=0o755)
        self.backup_dir.chmod(0o755)
        result = self._run("backup-compose.sh", self.backup_dir)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not be accessible by group or others", result.stderr)
        self.assertEqual(self._events(), [])

    def test_restore_leaves_service_stopped_after_restore_failure(self):
        result = self._run("restore-compose.sh", self.archive, fail_run_at=1)
        self.assertEqual(result.returncode, 42)
        self.assertIn("left stopped", result.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run"])

    def test_successful_restore_restarts_service(self):
        result = self._run("restore-compose.sh", self.archive)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run", "start"])

    def test_restore_rejects_a_writable_archive_before_touching_the_service(self):
        self.archive.chmod(0o666)
        result = self._run("restore-compose.sh", self.archive)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not be accessible by group or others", result.stderr)
        self.assertEqual(self._events(), [])

    def test_restore_rejects_a_readable_archive_before_touching_the_service(self):
        self.archive.chmod(0o644)
        result = self._run("restore-compose.sh", self.archive)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not be accessible by group or others", result.stderr)
        self.assertEqual(self._events(), [])

    def test_restore_refuses_legacy_archive_before_touching_the_service(self):
        legacy = self.root / "legacy.tar.gz"
        create_raw_archive(self.archive, legacy, legacy=True)
        result = self._run("restore-compose.sh", legacy)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--accept-federation-key-loss", result.stderr)
        self.assertEqual(self._events(), [])

    def test_restore_requires_explicit_key_loss_acknowledgement_for_legacy_archive(self):
        legacy = self.root / "legacy.tar.gz"
        create_raw_archive(self.archive, legacy, legacy=True)
        result = self._run("restore-compose.sh", "--accept-federation-key-loss", legacy)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("new identity will be created", result.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run", "start"])

    def test_restore_does_not_create_automatic_pre_restore_backup(self):
        result = self._run("restore-compose.sh", self.archive)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.run_count.read_text().strip(), "1")

    def test_scripts_support_podman_compose_when_selected(self):
        podman = self.bin_dir / "podman"
        podman.symlink_to(self.bin_dir / "docker")
        env = os.environ.copy()
        env.update(
            {
                "PATH": f"{self.bin_dir}:{env['PATH']}",
                "MOCK_LOG": str(self.log),
                "MOCK_RUN_COUNT": str(self.run_count),
                "MOCK_FAIL_RUN_AT": "0",
                "MOCK_RUNNING": "no",
                "MOCK_ARCHIVE": str(self.raw_archive),
                "COOKBOOK_BACKUP_DIR": str(self.backup_dir),
                "COOKBOOK_COMPOSE_ENGINE": "podman",
            }
        )
        result = subprocess.run([str(ROOT / "scripts" / "backup-compose.sh"), str(self.backup_dir)], cwd=ROOT, env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run"])


if __name__ == "__main__":
    unittest.main()
