import io
import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def create_valid_archive(path):
    with tarfile.open(path, mode="w:gz") as archive:
        database = tarfile.TarInfo("cookbook.db")
        payload = b"database snapshot"
        database.size = len(payload)
        archive.addfile(database, io.BytesIO(payload))
        media = tarfile.TarInfo("media/")
        media.type = tarfile.DIRTYPE
        archive.addfile(media)


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
        self._create_fake_docker()
        create_valid_archive(self.archive)

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
        self.assertIn("must not be group- or world-writable", result.stderr)
        self.assertEqual(self._events(), [])

    def test_restore_leaves_service_stopped_after_restore_failure(self):
        result = self._run("restore-compose.sh", self.archive, fail_run_at=2)
        self.assertEqual(result.returncode, 42)
        self.assertIn("left stopped", result.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run", "start", "ps", "stop", "run"])

    def test_successful_restore_restarts_service(self):
        result = self._run("restore-compose.sh", self.archive)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._events(), ["ps", "stop", "run", "start", "ps", "stop", "run", "start"])

    def test_restore_rejects_a_writable_archive_before_touching_the_service(self):
        self.archive.chmod(0o666)
        result = self._run("restore-compose.sh", self.archive)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not be group- or world-writable", result.stderr)
        self.assertEqual(self._events(), [])


if __name__ == "__main__":
    unittest.main()
