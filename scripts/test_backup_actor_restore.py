#!/usr/bin/env python3
"""Opt-in Podman cold restore proof using a generated active Person actor."""

import base64
import io
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
import uuid
import tarfile
import sqlite3
import time
from pathlib import Path

from validate_backup import finalize_backup, validate_backup

ROOT = Path(__file__).resolve().parents[1]
BACKEND = ROOT / "cookbook_back-end"
RUN_GO = ROOT.parent / "engineering-local" / "tools" / "run-go"


@unittest.skipUnless(os.environ.get("COOKBOOK_RUN_BACKUP_ACTOR_RESTORE") == "1",
                     "set COOKBOOK_RUN_BACKUP_ACTOR_RESTORE=1 to run the disposable Podman proof")
class ActorColdRestoreProof(unittest.TestCase):
    def test_generated_actor_survives_cold_restore_and_restart(self):
        if not shutil.which("podman"):
            self.fail("Podman is required for this opt-in proof")
        suffix = uuid.uuid4().hex[:16]
        project = f"cookbookactor{suffix}"
        image_tag = f"localhost/cookbook-backup-actor-proof:{suffix}"
        image_id = None
        temporary = tempfile.TemporaryDirectory(prefix="cookbook-actor-proof-")
        root = Path(temporary.name)
        compose_file = root / "compose.yaml"
        archive = root / "actor-backup.tar.gz"
        database = root / "generated.db"
        build_context = root / "build-context"
        build_context.mkdir()
        committed_backend = build_context / "cookbook_back-end"
        committed_backend.mkdir()
        source_archive = subprocess.run(["git", "archive", "--format=tar", "HEAD", "cookbook_back-end"],
                                        cwd=ROOT, capture_output=True, check=True).stdout
        with tarfile.open(fileobj=io.BytesIO(source_archive), mode="r:") as source:
            source.extractall(build_context)
        for filename in ("Dockerfile", ".dockerignore"):
            source_file = subprocess.run(["git", "show", f"HEAD:{filename}"], cwd=ROOT,
                                         capture_output=True, check=True).stdout
            (build_context / filename).write_bytes(source_file)
        env = os.environ.copy()
        env.update({"COMPOSE_FILE": str(compose_file), "COMPOSE_PROJECT_NAME": project,
                    "COOKBOOK_COMPOSE_ENGINE": "podman", "COOKBOOK_COMPOSE_SERVICE": "cookbook"})

        def run(args, *, cwd=ROOT, timeout=180, input_text=None):
            result = subprocess.run(args, cwd=cwd, env=env, text=True, input=input_text,
                                    capture_output=True, timeout=timeout)
            if result.returncode:
                raise AssertionError(f"command failed ({args[0]}): {result.stderr[-2000:]}")
            return result.stdout.strip()

        try:
            # Build this exact checkout as a private local fixture image. The build is offline.
            run(["podman", "build", "--pull=never", "--network=none", "--tag", image_tag, "-f", "Dockerfile", "."],
                cwd=build_context, timeout=600)
            image_id = run(["podman", "image", "inspect", "--format", "{{.Id}}", image_tag])
            if not re.fullmatch(r"(?:sha256:)?[0-9a-f]{64}", image_id):
                self.fail(f"fixture service image did not resolve to an immutable image ID: {image_id!r}")

            # Build the actor through the product's Go creation function in a disposable source copy.
            fixture_backend = committed_backend
            fixture_test = fixture_backend / "backup_actor_fixture_test.go"
            fixture_test.write_text('''package main
import ("context"; "os"; "testing")
func TestBackupActorFixture(t *testing.T) {
 path := os.Getenv("COOKBOOK_FIXTURE_DATABASE")
 db, err := openDatabase(path); if err != nil { t.Fatal(err) }
 if _, err = db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
 ('fixture-user','fixture@example.test','Fixture','now'),
 ('fixture-retired','retired@example.test','Retired Fixture','now')`); err != nil { t.Fatal(err) }
 if err = configureFederationOrigin(db, "https://cookbook.example.org"); err != nil { t.Fatal(err) }
 if _, err = CreatePersonActor(context.Background(), db, "fixture-user"); err != nil { t.Fatal(err) }
 if _, err = CreatePersonActor(context.Background(), db, "fixture-retired"); err != nil { t.Fatal(err) }
 if err = db.Close(); err != nil { t.Fatal(err) }
}
''', encoding="utf-8")
            fixture_env = env.copy()
            fixture_env["COOKBOOK_FIXTURE_DATABASE"] = str(database)
            generated = subprocess.run([str(RUN_GO), "test", "-run", "^TestBackupActorFixture$", "-count=1"],
                                       cwd=fixture_backend, env=fixture_env, text=True, capture_output=True, timeout=300)
            if generated.returncode:
                raise AssertionError(f"actor fixture generation failed: {(generated.stdout + generated.stderr)[-2000:]}")
            fixture_test.unlink()

            # Add an uncheckpointed WAL-only marker; the archive deliberately omits SQLite's transient -shm.
            connection = sqlite3.connect(database)
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("PRAGMA wal_autocheckpoint=0")
            connection.execute("CREATE TABLE proof_marker(value TEXT NOT NULL)")
            connection.execute("INSERT INTO proof_marker VALUES('cold-restore-proof')")
            connection.execute("UPDATE person_actor_ids SET state='retired',retired_at='proof' WHERE actor_id=(SELECT actor_id FROM person_actors WHERE user_id='fixture-retired')")
            connection.commit()
            if not Path(str(database) + "-wal").is_file():
                self.fail("fixture did not produce the required SQLite WAL")

            key = b"-----BEGIN PRIVATE KEY-----\n" + base64.b64encode(bytes.fromhex("302e020100300506032b657004220420" + "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")) + b"\n-----END PRIVATE KEY-----\n"
            with tarfile.open(archive, "w:gz") as output:
                for name, source in (("cookbook.db", database), ("cookbook.db-wal", Path(str(database) + "-wal"))):
                    if source.exists():
                        data = source.read_bytes()
                        entry = tarfile.TarInfo(name); entry.mode = 0o600; entry.size = len(data)
                        output.addfile(entry, io.BytesIO(data))
                media = tarfile.TarInfo("media/"); media.type = tarfile.DIRTYPE; media.mode = 0o700; output.addfile(media)
                secrets = tarfile.TarInfo("secrets/"); secrets.type = tarfile.DIRTYPE; secrets.mode = 0o700; output.addfile(secrets)
                key_info = tarfile.TarInfo("secrets/federation-ed25519.pk8.pem"); key_info.mode = 0o600; key_info.size = len(key)
                output.addfile(key_info, io.BytesIO(key))
            archive.chmod(0o600)
            connection.close()

            finalize_backup(archive, engine="podman", image=image_id)
            expected = validate_backup(archive, engine="podman", image=image_id)["person_actor_manifest"]
            states = {actor["state"] for actor in expected["actors"]}
            if states != {"active", "retired"}:
                self.fail("generated fixture did not contain active and WAL-retired actors")

            compose_file.write_text(f'''services:
  cookbook:
    image: {image_tag}
    restart: "no"
    entrypoint: ["/bin/sh", "-c", "sleep 3600"]
    volumes:
      - data:/data
volumes:
  data:
''', encoding="utf-8")
            run(["podman", "compose", "up", "-d", "cookbook"])
            run(["sh", str(ROOT / "scripts" / "restore-compose.sh"), str(archive)], timeout=300)
            compose_file.write_text(f'''services:
  cookbook:
    image: {image_tag}
    restart: "no"
    environment:
      COOKBOOK_PUBLIC_URL: http://localhost:8000
    volumes:
      - data:/data
volumes:
  data:
''', encoding="utf-8")
            run(["podman", "compose", "up", "-d", "--force-recreate", "cookbook"])
            ready = False
            last_probe_error = ""
            for _ in range(30):
                probe = subprocess.run(["podman", "compose", "exec", "-T", "cookbook", "cookbook", "instance", "recovery", "describe",
                                        "--database", "/data/cookbook.db"], cwd=ROOT, env=env,
                                       text=True, capture_output=True, timeout=10)
                if probe.returncode == 0:
                    ready = True
                    break
                last_probe_error = probe.stderr.lower()
                time.sleep(1)
            if not ready:
                logs = subprocess.run(["podman", "compose", "logs", "--tail", "20", "cookbook"],
                                      cwd=ROOT, env=env, text=True, capture_output=True, timeout=20)
                details = (logs.stdout + logs.stderr + last_probe_error).lower()
                if "permission denied" in details:
                    category = "fixture volume permission"
                elif "migration" in details or "database" in details or "sqlite" in details:
                    category = "database initialization"
                elif "exec format" in details or "no such file" in details:
                    category = "container entrypoint"
                elif "not running" in details or "no container" in details:
                    category = "fixture container exited"
                else:
                    category = "unclassified runtime readiness"
                raise AssertionError(f"fixture service did not become ready ({category})")
            recovered = json.loads(run(["podman", "compose", "exec", "-T", "cookbook", "cookbook", "instance", "recovery", "describe",
                                        "--database", "/data/cookbook.db"]))
            self.assertEqual(recovered, expected)
            container_id = run(["podman", "compose", "ps", "-q", "cookbook"])
            recovered_snapshot = root / "recovered-snapshot"
            recovered_snapshot.mkdir(mode=0o700)
            run(["podman", "cp", f"{container_id}:/data/cookbook.db", str(recovered_snapshot / "cookbook.db")])
            for sidecar in ("cookbook.db-wal", "cookbook.db-shm", "cookbook.db-journal"):
                exists = subprocess.run(["podman", "exec", container_id, "test", "-f", f"/data/{sidecar}"],
                                        cwd=ROOT, env=env, capture_output=True, timeout=10)
                if exists.returncode == 0:
                    run(["podman", "cp", f"{container_id}:/data/{sidecar}", str(recovered_snapshot / sidecar)])
            with sqlite3.connect(recovered_snapshot / "cookbook.db") as restored_db:
                marker = restored_db.execute("SELECT value FROM proof_marker").fetchone()
            self.assertEqual(marker, ("cold-restore-proof",))
            self.assertEqual(run(["podman", "compose", "ps", "--status", "running", "--services"]), "cookbook")
        finally:
            try:
                subprocess.run(["podman", "compose", "down", "--volumes", "--remove-orphans"], cwd=ROOT,
                               env=env, capture_output=True, timeout=60, check=False)
            finally:
                if image_id:
                    current = subprocess.run(["podman", "image", "inspect", "--format", "{{.Id}}", image_tag],
                                             capture_output=True, text=True, timeout=20, check=False)
                    if current.returncode == 0 and current.stdout.strip() == image_id:
                        subprocess.run(["podman", "image", "rm", image_id], capture_output=True, timeout=30, check=False)
                temporary.cleanup()


if __name__ == "__main__":
    unittest.main()
