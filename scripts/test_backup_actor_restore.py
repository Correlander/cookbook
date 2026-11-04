#!/usr/bin/env python3
"""Opt-in container-engine cold restore proof using a generated active Person actor."""

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


def cleanup_owned_images(image_tag, image_id, layer_label, run_command, *, engine="podman"):
    errors = []

    if image_id:
        try:
            inspect = run_command([engine, "image", "inspect", "--format", "{{.Id}}", image_tag])
            if inspect.returncode:
                raise RuntimeError(f"tag inspection failed: {inspect.stderr[-500:]}")
            if inspect.stdout.strip() != image_id:
                raise RuntimeError("fixture image tag no longer resolves to the inspected image ID")
            removal = run_command([engine, "image", "rm", image_id])
            if removal.returncode:
                raise RuntimeError(f"fixture image removal failed: {removal.stderr[-500:]}")
        except Exception as exc:
            errors.append(str(exc))

    label_key, separator, label_value = layer_label.partition("=")
    if not separator or not label_key or not label_value:
        errors.append("invalid temporary layer label")
    else:
        def list_owned_layers():
            result = run_command([
                engine, "image", "ls", "--all", "--no-trunc", "--format", "{{.Id}}",
                "--filter", f"label={layer_label}",
            ])
            if result.returncode:
                raise RuntimeError(f"owned layer enumeration failed: {result.stderr[-500:]}")
            image_ids = {line.strip() for line in result.stdout.splitlines() if line.strip()}
            if any(not re.fullmatch(r"(?:sha256:)?[0-9a-f]{64}", image_id) for image_id in image_ids):
                raise RuntimeError("owned layer enumeration returned an invalid image ID")
            return image_ids

        try:
            remaining = list_owned_layers()
            removal_errors = {}
            while remaining:
                before = remaining
                for owned_id in sorted(before):
                    if owned_id not in list_owned_layers():
                        removal_errors.pop(owned_id, None)
                        continue
                    template = '{{.Id}}|{{index .Config.Labels "' + label_key + '"}}'
                    verification = run_command([engine, "image", "inspect", "--format", template, owned_id])
                    if verification.returncode:
                        if owned_id in list_owned_layers():
                            removal_errors[owned_id] = f"ownership verification failed: {verification.stderr[-500:]}"
                        else:
                            removal_errors.pop(owned_id, None)
                        continue
                    verified_id, separator, verified_label = verification.stdout.strip().partition("|")
                    if not separator or verified_id.removeprefix("sha256:") != owned_id.removeprefix("sha256:") or verified_label != label_value:
                        removal_errors[owned_id] = "ownership verification failed; image left untouched"
                        continue
                    removal = run_command([engine, "image", "rm", owned_id])
                    still_owned = owned_id in list_owned_layers()
                    if removal.returncode and still_owned:
                        removal_errors[owned_id] = f"image removal failed: {removal.stderr[-500:]}"
                    elif not removal.returncode and still_owned:
                        removal_errors[owned_id] = "image removal returned success but the labeled image remains"
                    else:
                        removal_errors.pop(owned_id, None)
                remaining = list_owned_layers()
                if remaining and remaining == before:
                    break
            if remaining:
                details = [f"{image_id}: {removal_errors.get(image_id, 'removal made no progress')}" for image_id in sorted(remaining)]
                errors.append("owned layers remain after cleanup: " + "; ".join(details))
        except Exception as exc:
            errors.append(str(exc))

    if errors:
        raise RuntimeError("; ".join(errors))


def build_image_args(engine, image_tag, layer_label):
    if engine == "podman":
        return [engine, "build", "--pull=never", "--network=none", "--layer-label", layer_label,
                "--tag", image_tag, "-f", "Dockerfile", "."]
    if engine == "docker":
        return [engine, "build", "--label", layer_label, "--tag", image_tag,
                "-f", "Dockerfile", "."]
    raise ValueError("engine must be podman or docker")


class _FakeImageEngine:
    def __init__(self, labels, *, engine="podman", fail_query=False, fail_removals=()):
        self.labels = dict(labels)
        self.engine = engine
        self.fail_query = fail_query
        self.fail_removals = set(fail_removals)
        self.commands = []
        self.removed = []

    def __call__(self, args):
        self.commands.append(args)
        if args[:3] == [self.engine, "image", "ls"]:
            if "--all" not in args:
                return subprocess.CompletedProcess(args, 2, "", "missing --all")
            if self.fail_query:
                return subprocess.CompletedProcess(args, 125, "", "injected image query failure")
            label_filter = next((item for item in args if item.startswith("label=")), "")
            wanted = label_filter.removeprefix("label=")
            ids = sorted(image_id for image_id, label in self.labels.items() if label == wanted)
            return subprocess.CompletedProcess(args, 0, "\n".join(ids), "")
        if args[:3] == [self.engine, "image", "inspect"]:
            image_id = args[-1]
            if image_id not in self.labels:
                return subprocess.CompletedProcess(args, 1, "", "image not found")
            label = self.labels[image_id]
            return subprocess.CompletedProcess(args, 0, f'{image_id}|{label.split("=", 1)[1]}', "")
        if args[:3] == [self.engine, "image", "rm"]:
            image_id = args[3]
            if image_id in self.fail_removals:
                return subprocess.CompletedProcess(args, 125, "", "injected image removal failure")
            self.removed.append(image_id)
            self.labels.pop(image_id, None)
            return subprocess.CompletedProcess(args, 0, image_id, "")
        return subprocess.CompletedProcess(args, 2, "", f"unexpected fake {self.engine} command")


class OwnedImageCleanupTests(unittest.TestCase):
    def test_cleanup_uses_all_layers_and_preserves_unrelated_images(self):
        run_label = "com.cookbook.test-run=run-123"
        engine = _FakeImageEngine({
            "sha256:" + "a" * 64: run_label,
            "sha256:" + "b" * 64: run_label,
            "sha256:" + "c" * 64: "com.cookbook.test-run=other-run",
        })
        cleanup_owned_images("unused", None, run_label, engine)
        self.assertEqual(set(engine.removed), {"sha256:" + "a" * 64, "sha256:" + "b" * 64})
        self.assertIn("sha256:" + "c" * 64, engine.labels)
        self.assertTrue(all("--all" in command for command in engine.commands if command[:3] == ["podman", "image", "ls"]))

    def test_cleanup_surfaces_owned_image_query_failure(self):
        engine = _FakeImageEngine({}, fail_query=True)
        with self.assertRaisesRegex(RuntimeError, "owned layer enumeration failed"):
            cleanup_owned_images("unused", None, "com.cookbook.test-run=run-123", engine)

    def test_cleanup_surfaces_intermediate_image_removal_failure(self):
        run_label = "com.cookbook.test-run=run-123"
        image_id = "sha256:" + "a" * 64
        engine = _FakeImageEngine({image_id: run_label}, fail_removals={image_id})
        with self.assertRaisesRegex(RuntimeError, "image removal failed"):
            cleanup_owned_images("unused", None, run_label, engine)

    def test_docker_cleanup_uses_only_images_with_this_run_label(self):
        run_label = "com.cookbook.test-run=run-456"
        image_id = "sha256:" + "d" * 64
        engine = _FakeImageEngine({image_id: run_label}, engine="docker")
        cleanup_owned_images("unused", None, run_label, engine, engine="docker")
        self.assertEqual(engine.removed, [image_id])
        self.assertTrue(all(command[0] == "docker" for command in engine.commands))

    def test_build_image_args_match_each_engine(self):
        self.assertEqual(
            build_image_args("podman", "local/image:proof", "proof=run-123"),
            ["podman", "build", "--pull=never", "--network=none", "--layer-label", "proof=run-123",
             "--tag", "local/image:proof", "-f", "Dockerfile", "."],
        )
        docker_args = build_image_args("docker", "local/image:proof", "proof=run-123")
        self.assertEqual(
            docker_args,
            ["docker", "build", "--label", "proof=run-123", "--tag", "local/image:proof",
             "-f", "Dockerfile", "."],
        )
        self.assertNotIn("--network=none", docker_args)


@unittest.skipUnless(os.environ.get("COOKBOOK_RUN_BACKUP_ACTOR_RESTORE") == "1",
                     "set COOKBOOK_RUN_BACKUP_ACTOR_RESTORE=1 to run the disposable container proof")
class ActorColdRestoreProof(unittest.TestCase):
    def test_generated_actor_survives_cold_restore_and_restart(self):
        engine = os.environ.get("COOKBOOK_CONTAINER_ENGINE", "podman")
        if engine not in ("podman", "docker"):
            self.fail("COOKBOOK_CONTAINER_ENGINE must be podman or docker")
        if not shutil.which(engine):
            self.fail(f"{engine} is required for this opt-in proof")
        suffix = uuid.uuid4().hex[:16]
        project = f"cookbookactor{suffix}"
        shared_image = os.environ.get("COOKBOOK_BACKUP_ACTOR_IMAGE")
        image_tag = shared_image or f"localhost/cookbook-backup-actor-proof:{suffix}"
        owns_image = shared_image is None
        layer_label = f"com.cookbook.test-run={suffix}"
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
                    "COOKBOOK_COMPOSE_ENGINE": engine, "COOKBOOK_COMPOSE_SERVICE": "cookbook"})

        def run(args, *, cwd=ROOT, timeout=180, input_text=None):
            result = subprocess.run(args, cwd=cwd, env=env, text=True, input=input_text,
                                    capture_output=True, timeout=timeout)
            if result.returncode:
                raise AssertionError(f"command failed ({args[0]}): {result.stderr[-2000:]}")
            return result.stdout.strip()

        try:
            if owns_image:
                # Podman reuses its cached base image offline; Docker pulls missing base layers on fresh CI runners.
                build_args = build_image_args(engine, image_tag, layer_label)
                run(build_args, cwd=build_context, timeout=600)
            image_id = run([engine, "image", "inspect", "--format", "{{.Id}}", image_tag])
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
            go_command = str(RUN_GO) if RUN_GO.is_file() else shutil.which("go")
            if not go_command:
                self.fail("Go is required to generate the actor fixture")
            generated = subprocess.run([go_command, "test", "-run", "^TestBackupActorFixture$", "-count=1"],
                                       cwd=fixture_backend, env=fixture_env, text=True, capture_output=True, timeout=300)
            if generated.returncode:
                raise AssertionError(f"actor fixture generation failed: {(generated.stdout + generated.stderr)[-2000:]}")
            fixture_test.unlink()
            source_db = sqlite3.connect(database)
            try:
                source_schema_version = source_db.execute("SELECT MAX(version) FROM schema_migrations").fetchone()
            finally:
                source_db.close()
            if source_schema_version is None or source_schema_version[0] is None:
                self.fail("generated source database has no migration version")

            # Add an uncheckpointed WAL-only marker; the archive deliberately omits SQLite's transient -shm.
            connection = sqlite3.connect(database)
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("PRAGMA wal_autocheckpoint=0")
            connection.execute("PRAGMA foreign_keys=ON")
            connection.execute("CREATE TABLE proof_marker(value TEXT NOT NULL)")
            connection.execute("INSERT INTO proof_marker VALUES('cold-restore-proof')")
            connection.execute("UPDATE person_actor_ids SET state='retired',retired_at='proof' WHERE actor_id=(SELECT actor_id FROM person_actors WHERE user_id='fixture-retired')")
            profile_id = "a" * 32
            local_attribution_id = "b" * 32
            foreign_source_id = "c" * 32
            foreign_creator_id = "d" * 32
            external_attribution_id = "e" * 32
            library_local_instance = "1" * 32
            library_foreign_instance = "2" * 32
            saved_local_time = "2026-11-04T02:30:00Z"
            saved_foreign_time = "2026-11-04T02:31:00Z"
            connection.execute(
                """INSERT INTO creator_profiles
                   (creator_id,user_id,public_name,bio,visibility,created_at,updated_at)
                   VALUES(?,?,?,?,?,?,?)""",
                (profile_id, "fixture-user", "Restored Creator", "v7 profile proof", "private", "created", "updated"),
            )
            connection.execute(
                """INSERT INTO recipes
                   (id,owner_id,creator_name,title,visibility,edit_policy,version,created_at,updated_at)
                   VALUES(?,?,?,?,?,?,?,?,?)""",
                ("profile-proof-recipe", "fixture-user", "", "Profile recovery proof", "private", "owner_only", 1, "created", "updated"),
            )
            connection.execute(
                """INSERT INTO content_attributions
                   (id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
                   VALUES(?,?,'local',?,'creator','self_asserted_at_creation','created')""",
                (local_attribution_id, "profile-proof-recipe", profile_id),
            )
            connection.execute(
                """INSERT INTO content_attributions
                   (id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
                   VALUES(?,?,'external_claim',?,?,?,'contributor','portable_import','created')""",
                (external_attribution_id, "profile-proof-recipe", foreign_source_id, foreign_creator_id, "Foreign Claim"),
            )
            connection.execute(
                """INSERT INTO cookbook_bookmarks
                   (id,user_id,local_instance_id,local_cookbook_id,created_at)
                   VALUES(?,?,?,?,?),(?,?,?,?,?)""",
                ("local-saved-book", "fixture-user", library_local_instance,
                 "local-source-book", saved_local_time,
                 "foreign-saved-book", "fixture-user", library_foreign_instance,
                 "foreign-source-book", saved_foreign_time),
            )
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

            finalize_backup(archive, engine=engine, image=image_id)
            expected = validate_backup(archive, engine=engine, image=image_id)["person_actor_manifest"]
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
            run([engine, "compose", "up", "-d", "cookbook"])
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
            run([engine, "compose", "up", "-d", "--force-recreate", "cookbook"])
            ready = False
            last_probe_error = ""
            for _ in range(30):
                probe = subprocess.run([engine, "compose", "exec", "-T", "cookbook", "cookbook", "instance", "recovery", "describe",
                                        "--database", "/data/cookbook.db"], cwd=ROOT, env=env,
                                       text=True, capture_output=True, timeout=10)
                if probe.returncode == 0:
                    ready = True
                    break
                last_probe_error = probe.stderr.lower()
                time.sleep(1)
            if not ready:
                logs = subprocess.run([engine, "compose", "logs", "--tail", "20", "cookbook"],
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
            recovered = json.loads(run([engine, "compose", "exec", "-T", "cookbook", "cookbook", "instance", "recovery", "describe",
                                        "--database", "/data/cookbook.db"]))
            self.assertEqual(recovered, expected)
            container_id = run([engine, "compose", "ps", "-q", "cookbook"])
            recovered_snapshot = root / "recovered-snapshot"
            recovered_snapshot.mkdir(mode=0o700)
            run([engine, "cp", f"{container_id}:/data/cookbook.db", str(recovered_snapshot / "cookbook.db")])
            for sidecar in ("cookbook.db-wal", "cookbook.db-shm", "cookbook.db-journal"):
                exists = subprocess.run([engine, "exec", container_id, "test", "-f", f"/data/{sidecar}"],
                                        cwd=ROOT, env=env, capture_output=True, timeout=10)
                if exists.returncode == 0:
                    run([engine, "cp", f"{container_id}:/data/{sidecar}", str(recovered_snapshot / sidecar)])
            with sqlite3.connect(recovered_snapshot / "cookbook.db") as restored_db:
                marker = restored_db.execute("SELECT value FROM proof_marker").fetchone()
            self.assertEqual(marker, ("cold-restore-proof",))
            with sqlite3.connect(recovered_snapshot / "cookbook.db") as restored_db:
                restored_db.execute("PRAGMA foreign_keys=ON")
                version = restored_db.execute("SELECT MAX(version) FROM schema_migrations").fetchone()
                self.assertEqual(version, source_schema_version)
                self.assertEqual(restored_db.execute("PRAGMA integrity_check").fetchone(), ("ok",))
                self.assertEqual(restored_db.execute("PRAGMA foreign_key_check").fetchall(), [])
                saved_references = restored_db.execute(
                    "SELECT id,user_id,local_instance_id,local_cookbook_id,created_at "
                    "FROM cookbook_bookmarks ORDER BY id"
                ).fetchall()
                self.assertEqual(saved_references, [
                    ("foreign-saved-book", "fixture-user", library_foreign_instance,
                     "foreign-source-book", saved_foreign_time),
                    ("local-saved-book", "fixture-user", library_local_instance,
                     "local-source-book", saved_local_time),
                ])
                tables = {row[0] for row in restored_db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
                self.assertTrue({"creator_profiles", "content_attributions"}.issubset(tables))
                profile = restored_db.execute(
                    "SELECT creator_id,user_id,public_name,bio,visibility,created_at,updated_at "
                    "FROM creator_profiles WHERE creator_id=?", (profile_id,),
                ).fetchone()
                self.assertEqual(profile, (profile_id, "fixture-user", "Restored Creator", "v7 profile proof", "private", "created", "updated"))
                local_attribution = restored_db.execute(
                    "SELECT recipe_id,cookbook_id,subject_kind,creator_id,role,provenance "
                    "FROM content_attributions WHERE id=?", (local_attribution_id,),
                ).fetchone()
                self.assertEqual(local_attribution, ("profile-proof-recipe", None, "local", profile_id, "creator", "self_asserted_at_creation"))
                external_attribution = restored_db.execute(
                    "SELECT recipe_id,cookbook_id,subject_kind,creator_id,claim_source_instance_id,claim_creator_id,claim_name,role,provenance "
                    "FROM content_attributions WHERE id=?", (external_attribution_id,),
                ).fetchone()
                self.assertEqual(
                    external_attribution,
                    ("profile-proof-recipe", None, "external_claim", None, foreign_source_id, foreign_creator_id, "Foreign Claim", "contributor", "portable_import"),
                )
                with self.assertRaises(sqlite3.IntegrityError):
                    restored_db.execute("DELETE FROM creator_profiles WHERE creator_id=?", (profile_id,))
                with self.assertRaises(sqlite3.IntegrityError):
                    restored_db.execute(
                        """INSERT INTO content_attributions
                           (id,subject_kind,creator_id,role,provenance,created_at)
                           VALUES(?,'local',?,'creator','self_asserted_at_creation','created')""",
                        ("f" * 32, profile_id),
                    )
                with self.assertRaises(sqlite3.IntegrityError):
                    restored_db.execute(
                        """INSERT INTO content_attributions
                           (id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
                           VALUES(?,?,'local',?,'creator','self_asserted_at_creation','created')""",
                        ("f" * 32, "profile-proof-recipe", profile_id),
                    )
                self.assertEqual(
                    restored_db.execute("SELECT COUNT(*) FROM creator_profiles WHERE creator_id=?", (profile_id,)).fetchone(),
                    (1,),
                )
            self.assertEqual(run([engine, "compose", "ps", "--status", "running", "--services"]), "cookbook")
        finally:
            try:
                if compose_file.exists():
                    stopped = subprocess.run([engine, "compose", "down", "--volumes", "--remove-orphans"], cwd=ROOT,
                                             env=env, capture_output=True, text=True, timeout=60, check=False)
                    if stopped.returncode:
                        raise RuntimeError(f"fixture compose cleanup failed: {stopped.stderr[-1000:]}")
            finally:
                try:
                    if owns_image:
                        cleanup_owned_images(
                            image_tag, image_id, layer_label,
                            lambda args: subprocess.run(args, capture_output=True, text=True, timeout=30),
                            engine=engine,
                        )
                finally:
                    temporary.cleanup()


if __name__ == "__main__":
    unittest.main()
