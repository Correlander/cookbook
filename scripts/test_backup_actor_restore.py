#!/usr/bin/env python3
"""Opt-in container-engine cold restore proof using a generated active Person actor."""

import base64
import io
import hashlib
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
from contextlib import closing
from pathlib import Path

from validate_backup import finalize_backup, validate_backup

ROOT = Path(__file__).resolve().parents[1]
BACKEND = ROOT / "cookbook_back-end"
RUN_GO = ROOT.parent / "engineering-local" / "tools" / "run-go"


def seed_snapshot_recovery_fixtures(connection):
    """Storage-only fixture; this does not exercise cryptographic ingestion."""
    source = "https://peer.example.org"
    claim = b"opaque-imported-claim-v1\x00fixture"
    payload = b'{"fixture":"preserved snapshot payload"}'
    payload_hash = hashlib.sha256(payload).digest()
    event = b"synthetic signed event bytes"
    event_hash = hashlib.sha256(event).digest()
    base = {
        "object_uri": source + "/federation/recipes/recovery",
        "source_origin": source,
        "source_recipe_id": "recovery",
        "sequence": 17,
        "payload_json": payload,
        "payload_sha256": payload_hash,
        "creator_name": "Recovery fixture",
        "received_at": "2026-11-04T00:00:00Z",
        "event_id": "fixture-event-17",
        "event_type": "recipe.snapshot",
        "provenance_state": "publisher_verified",
        "trusted_receipt_id": b"fixture-receipt",
        "grant_purpose": "personal_private_retention",
        "grant_retention": "continues_after_ordinary_withdrawal",
        "grant_portability": "same_person_personal_account_only",
        "event_body": event,
        "event_body_sha256": event_hash,
        "signature_input": "fixture signature input",
        "signature_value": "fixture signature",
        "request_method": "POST",
        "request_target": "/api/federation/inbox",
        "covered_components": "@method @target-uri",
        "covered_values": "fixture covered values",
        "content_digest": "sha-256=:fixture:",
        "authenticated_origin": source,
        "key_id": "fixture-key",
        "public_key": bytes(range(32)),
        "key_status_at_receipt": "active",
        "key_created_at": "2026-11-01T00:00:00Z",
        "profile_at_receipt": "exchange-v2-retention",
        "accepted_at": "2026-11-04T00:00:00Z",
        "evidence_bytes": len(event) + 128,
        "encoded_capture_bytes": len(payload) + len(event) + 256,
        "title": "Preserved snapshot",
        "description": "Recovery fixture",
        "tags_json": "[]",
        "untrusted_claim": None,
    }
    columns = tuple(base)
    sql = ("INSERT INTO library_remote_snapshot_versions(version_id," + ",".join(columns) + ") VALUES(" +
           ",".join("?" for _ in range(len(columns) + 1)) + ")")
    states = ("active", "restricted", "removed", "imported")
    for state in states:
        bookmark = "snapshot-" + state
        uri = source + "/federation/recipes/" + state
        connection.execute(
            "INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES(?,?,'remote',?,?)",
            (bookmark, "fixture-user", uri, "2026-11-04T00:00:00Z"),
        )
        if state == "removed":
            connection.execute(
                "INSERT INTO library_remote_snapshot_pins(bookmark_id,version_id,state,created_at,removed_version_id,moderated_by,moderated_at,moderation_reason,moderation_outcome) VALUES(?,NULL,'removed',?,'removed-version','fixture-admin','2026-11-04T00:00:00Z','privacy','removed')",
                (bookmark, "2026-11-04T00:00:00Z"),
            )
            continue
        version_id = "version-" + state
        row = dict(base, object_uri=uri, source_recipe_id=state)
        if state == "imported":
            row.update(sequence=None, event_id=None, event_type=None, provenance_state="user_imported_unverified",
                       untrusted_claim=claim)
            for name in ("trusted_receipt_id", "grant_purpose", "grant_retention", "grant_portability",
                         "event_body", "event_body_sha256", "signature_input", "signature_value", "request_method",
                         "request_target", "covered_components", "covered_values", "content_digest", "authenticated_origin",
                         "key_id", "public_key", "key_status_at_receipt", "key_created_at", "profile_at_receipt", "accepted_at", "evidence_bytes"):
                row[name] = None
        connection.execute(sql, (version_id, *(row[name] for name in columns)))
        if state == "restricted":
            connection.execute(
                "INSERT INTO library_remote_snapshot_pins(bookmark_id,version_id,state,created_at,moderated_by,moderated_at,moderation_reason,moderation_outcome) VALUES(?,?,'restricted',?,'fixture-admin','2026-11-04T00:00:00Z','policy','restricted')",
                (bookmark, version_id, "2026-11-04T00:00:00Z"),
            )
        else:
            connection.execute(
                "INSERT INTO library_remote_snapshot_pins(bookmark_id,version_id,state,created_at) VALUES(?,?,'active',?)",
                (bookmark, version_id, "2026-11-04T00:00:00Z"),
            )
    connection.execute(
        "INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'created','updated')",
        (source,),
    )
    connection.execute(
        "INSERT INTO federation_peer_keys(key_id,origin,public_key,status,created_at) VALUES('fixture-key',?,?, 'active','created')",
        (source, bytes(range(32))),
    )
    # Snapshot evidence is self-contained after its independent peer/key records are removed.
    if connection.execute("DELETE FROM federation_peer_keys WHERE origin=?", (source,)).rowcount != 1:
        raise AssertionError("synthetic peer key was not present for removal")
    if connection.execute("DELETE FROM federation_peers WHERE origin=?", (source,)).rowcount != 1:
        raise AssertionError("synthetic peer was not present for removal")
    return claim, payload, event, payload_hash, event_hash


def read_menu_recovery_state(connection, menu_ids):
    """Return the exact persisted menu contract fields for a recovery fixture."""
    placeholders = ",".join("?" for _ in menu_ids)
    menus = connection.execute(
        "SELECT id,owner_id,title,note,creator_name,version,created_at,updated_at "
        "FROM meal_menus WHERE id IN (" + placeholders + ") ORDER BY id",
        menu_ids,
    ).fetchall()
    entries = connection.execute(
        "SELECT id,menu_id,position,local_instance_id,local_recipe_id,label,note,serving_note "
        "FROM meal_menu_entries WHERE menu_id IN (" + placeholders + ") ORDER BY menu_id,position,id",
        menu_ids,
    ).fetchall()
    attributions = connection.execute(
        "SELECT id,recipe_id,cookbook_id,course_id,menu_id,subject_kind,creator_id,"
        "claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at "
        "FROM content_attributions WHERE menu_id IN (" + placeholders + ") ORDER BY id",
        menu_ids,
    ).fetchall()
    return menus, entries, attributions


def create_sqlite_online_snapshot(source, destination):
    """Create and check one coherent, closed SQLite online-backup snapshot."""
    target = sqlite3.connect(destination)
    try:
        target.execute("PRAGMA journal_mode=DELETE")
        source.backup(target)
        integrity = target.execute("PRAGMA integrity_check").fetchone()
        if integrity != ("ok",):
            raise AssertionError(f"online backup integrity check failed: {integrity!r}")
        foreign_keys = target.execute("PRAGMA foreign_key_check").fetchall()
        if foreign_keys:
            raise AssertionError(f"online backup foreign-key check failed: {foreign_keys!r}")
    finally:
        target.close()


def write_sqlite_backup_archive(database_snapshot, archive_path, key):
    """Package a closed SQLite backup snapshot without copying live DB sidecars."""
    with tarfile.open(archive_path, "w:gz") as output:
        data = database_snapshot.read_bytes()
        database_entry = tarfile.TarInfo("cookbook.db")
        database_entry.mode = 0o600
        database_entry.size = len(data)
        output.addfile(database_entry, io.BytesIO(data))
        media = tarfile.TarInfo("media/")
        media.type = tarfile.DIRTYPE
        media.mode = 0o700
        output.addfile(media)
        secrets = tarfile.TarInfo("secrets/")
        secrets.type = tarfile.DIRTYPE
        secrets.mode = 0o700
        output.addfile(secrets)
        key_info = tarfile.TarInfo("secrets/federation-ed25519.pk8.pem")
        key_info.mode = 0o600
        key_info.size = len(key)
        output.addfile(key_info, io.BytesIO(key))
    archive_path.chmod(0o600)


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


class SQLiteOnlineBackupHelperTests(unittest.TestCase):
    def test_online_snapshot_captures_committed_wal_and_archive_uses_closed_snapshot(self):
        with tempfile.TemporaryDirectory(prefix="cookbook-sqlite-backup-helper-") as directory:
            root = Path(directory)
            source_path = root / "source.db"
            cold_path = root / "cold.db"
            hot_path = root / "hot.db"
            archive_path = root / "backup.tar.gz"
            source = sqlite3.connect(source_path)
            try:
                self.assertEqual(source.execute("PRAGMA journal_mode=WAL").fetchone(), ("wal",))
                source.execute("PRAGMA wal_autocheckpoint=0")
                source.execute("CREATE TABLE menu_fixture(value TEXT NOT NULL)")
                source.execute("INSERT INTO menu_fixture VALUES('checkpointed')")
                source.commit()
                self.assertEqual(source.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone(), (0, 0, 0))
                create_sqlite_online_snapshot(source, cold_path)

                source.execute("UPDATE menu_fixture SET value='committed-in-wal'")
                source.commit()
                wal_path = Path(str(source_path) + "-wal")
                self.assertTrue(wal_path.is_file())
                self.assertGreater(wal_path.stat().st_size, 0)
                with closing(sqlite3.connect(cold_path)) as cold:
                    self.assertEqual(cold.execute("SELECT value FROM menu_fixture").fetchone(), ("checkpointed",))
                create_sqlite_online_snapshot(source, hot_path)
            finally:
                source.close()

            with closing(sqlite3.connect(hot_path)) as hot:
                self.assertEqual(hot.execute("SELECT value FROM menu_fixture").fetchone(), ("committed-in-wal",))

            key = b"synthetic private key fixture"
            write_sqlite_backup_archive(hot_path, archive_path, key)
            with tarfile.open(archive_path, "r:gz") as archive:
                members = {member.name: member for member in archive.getmembers()}
                self.assertEqual(set(members), {
                    "cookbook.db", "media", "secrets", "secrets/federation-ed25519.pk8.pem",
                })
                self.assertEqual(members["cookbook.db"].mode & 0o777, 0o600)
                self.assertEqual(members["secrets/federation-ed25519.pk8.pem"].mode & 0o777, 0o600)
                self.assertEqual(archive.extractfile("secrets/federation-ed25519.pk8.pem").read(), key)
                extracted_db = root / "extracted.db"
                extracted_db.write_bytes(archive.extractfile("cookbook.db").read())
            with closing(sqlite3.connect(extracted_db)) as extracted:
                self.assertEqual(
                    extracted.execute("SELECT value FROM menu_fixture").fetchone(),
                    ("committed-in-wal",),
                )


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
        cold_archive = root / "actor-backup-cold.tar.gz"
        hot_archive = root / "actor-backup-hot-wal.tar.gz"
        database = root / "generated.db"
        cold_database = root / "cold-online-backup.db"
        hot_database = root / "hot-wal-online-backup.db"
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

        def copy_restored_database(snapshot_name, *, stop_service=False):
            snapshot = root / snapshot_name
            snapshot.mkdir(mode=0o700)
            if stop_service:
                run([engine, "compose", "stop", "cookbook"])
            container_id = run([engine, "compose", "ps", "-a", "-q", "cookbook"])
            run([engine, "cp", f"{container_id}:/data/cookbook.db", str(snapshot / "cookbook.db")])
            return snapshot / "cookbook.db"

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
            if source_schema_version[0] != 15:
                self.fail(f"generated source database schema version is {source_schema_version[0]}, expected 15")

            # Seed a checkpointed baseline, then prove a later committed menu edit is in WAL.
            connection = sqlite3.connect(database)
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("PRAGMA wal_autocheckpoint=0")
            connection.execute("PRAGMA foreign_keys=ON")
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
            fixture_instance_id = connection.execute(
                "SELECT instance_id FROM library_instance_identity WHERE singleton_id=1"
            ).fetchone()[0]
            if not re.fullmatch(r"[0-9a-f]{32}", fixture_instance_id):
                self.fail("fixture database did not contain its real library instance identity")
            local_menu_id = "f" * 32
            external_menu_id = "0" * 32
            menu_ids = (external_menu_id, local_menu_id)
            connection.execute(
                """INSERT INTO meal_menus
                   (id,owner_id,title,note,creator_name,version,created_at,updated_at)
                   VALUES(?,?,?,?,?,?,?,?),(?,?,?,?,?,?,?,?)""",
                (external_menu_id, "fixture-user", "Guest supper", "Keep the source note",
                 "Guest Author", 2, "menu-created-guest", "menu-updated-guest",
                 local_menu_id, "fixture-user", "Winter table", "Serve the courses in order",
                 "", 3, "menu-created-local", "menu-updated-local"),
            )
            local_entries = (
                ("1" * 32, local_menu_id, 0, fixture_instance_id, "profile-proof-recipe",
                 "Starter", "First serving note", "Serve warm"),
                ("2" * 32, local_menu_id, 1, None, None,
                 "Unresolved", "Keep this unavailable slot", "Choose a replacement later"),
                ("3" * 32, local_menu_id, 2, fixture_instance_id, "profile-proof-recipe",
                 "Second serving", "Same recipe, separate course", "Serve after the starter"),
                ("4" * 32, external_menu_id, 0, None, None,
                 "Guest slot", "Preserve this private note", "Ask the source"),
            )
            connection.executemany(
                """INSERT INTO meal_menu_entries
                   (id,menu_id,position,local_instance_id,local_recipe_id,label,note,serving_note)
                   VALUES(?,?,?,?,?,?,?,?)""",
                local_entries,
            )
            connection.execute(
                """INSERT INTO content_attributions
                   (id,menu_id,subject_kind,creator_id,role,provenance,created_at)
                   VALUES(?,?,'local',?,'creator','self_asserted_at_creation','menu-local-credit')""",
                ("5" * 32, local_menu_id, profile_id),
            )
            connection.execute(
                """INSERT INTO content_attributions
                   (id,menu_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,
                    role,provenance,created_at)
                   VALUES(?,?, 'external_claim',?,?,?,'creator','portable_import','menu-external-credit')""",
                ("6" * 32, external_menu_id, foreign_source_id, foreign_creator_id, "Guest Author"),
            )
            snapshot_claim, snapshot_payload, snapshot_event, snapshot_payload_hash, snapshot_event_hash = seed_snapshot_recovery_fixtures(connection)
            connection.commit()

            checkpoint = connection.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
            if checkpoint != (0, 0, 0):
                self.fail(f"fixture baseline did not checkpoint cleanly: {checkpoint!r}")
            cold_menu_expected = (
                [
                    (external_menu_id, "fixture-user", "Guest supper", "Keep the source note", "Guest Author", 2,
                     "menu-created-guest", "menu-updated-guest"),
                    (local_menu_id, "fixture-user", "Winter table", "Serve the courses in order", "", 3,
                     "menu-created-local", "menu-updated-local"),
                ],
                [
                    local_entries[3], local_entries[0], local_entries[1], local_entries[2],
                ],
                [
                    ("5" * 32, None, None, None, local_menu_id, "local", profile_id, None, None, None,
                     "creator", "self_asserted_at_creation", "menu-local-credit"),
                    ("6" * 32, None, None, None, external_menu_id, "external_claim", None,
                     foreign_source_id, foreign_creator_id, "Guest Author", "creator", "portable_import",
                     "menu-external-credit"),
                ],
            )
            create_sqlite_online_snapshot(connection, cold_database)
            with closing(sqlite3.connect(cold_database)) as cold_source:
                cold_menu_state = read_menu_recovery_state(cold_source, menu_ids)
                self.assertEqual(cold_menu_state, cold_menu_expected)
                self.assertEqual(
                    cold_source.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='proof_marker'").fetchone(),
                    (0,),
                )

            key = b"-----BEGIN PRIVATE KEY-----\n" + base64.b64encode(bytes.fromhex("302e020100300506032b657004220420" + "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")) + b"\n-----END PRIVATE KEY-----\n"
            write_sqlite_backup_archive(cold_database, cold_archive, key)

            connection.execute("CREATE TABLE proof_marker(value TEXT NOT NULL)")
            connection.execute("INSERT INTO proof_marker VALUES('hot-wal-restore-proof')")
            connection.execute(
                "UPDATE meal_menus SET version=4,updated_at='menu-updated-hot' WHERE id=?",
                (local_menu_id,),
            )
            connection.execute(
                "UPDATE meal_menu_entries SET note='WAL-updated unresolved slot note' WHERE id=?",
                ("2" * 32,),
            )
            connection.commit()
            wal_path = Path(str(database) + "-wal")
            if not wal_path.is_file() or wal_path.stat().st_size == 0:
                self.fail("committed menu update did not remain in the source WAL")
            hot_menu_expected = (
                [
                    cold_menu_expected[0][0],
                    (local_menu_id, "fixture-user", "Winter table", "Serve the courses in order", "", 4,
                     "menu-created-local", "menu-updated-hot"),
                ],
                [
                    local_entries[3], local_entries[0],
                    ("2" * 32, local_menu_id, 1, None, None,
                     "Unresolved", "WAL-updated unresolved slot note", "Choose a replacement later"),
                    local_entries[2],
                ],
                cold_menu_expected[2],
            )
            self.assertNotEqual(
                read_menu_recovery_state(connection, menu_ids),
                cold_menu_state,
                "the committed hot menu edit must differ from checkpointed cold state",
            )
            self.assertEqual(read_menu_recovery_state(connection, menu_ids), hot_menu_expected)
            create_sqlite_online_snapshot(connection, hot_database)
            with closing(sqlite3.connect(hot_database)) as hot_source:
                self.assertEqual(read_menu_recovery_state(hot_source, menu_ids), hot_menu_expected)
                self.assertEqual(
                    hot_source.execute("SELECT value FROM proof_marker").fetchone(),
                    ("hot-wal-restore-proof",),
                )
            write_sqlite_backup_archive(hot_database, hot_archive, key)
            connection.close()

            finalize_backup(cold_archive, engine=engine, image=image_id)
            cold_expected = validate_backup(cold_archive, engine=engine, image=image_id)["person_actor_manifest"]
            finalize_backup(hot_archive, engine=engine, image=image_id)
            expected = validate_backup(hot_archive, engine=engine, image=image_id)["person_actor_manifest"]
            self.assertEqual(cold_expected, expected, "cold and hot snapshots changed the actor manifest")
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
            run(["sh", str(ROOT / "scripts" / "restore-compose.sh"), str(cold_archive)], timeout=300)
            cold_restored_database = copy_restored_database("cold-restored-snapshot")
            with closing(sqlite3.connect(cold_restored_database)) as cold_restored:
                cold_restored.execute("PRAGMA foreign_keys=ON")
                self.assertEqual(read_menu_recovery_state(cold_restored, menu_ids), cold_menu_expected)
                self.assertEqual(cold_restored.execute("PRAGMA integrity_check").fetchone(), ("ok",))
                self.assertEqual(cold_restored.execute("PRAGMA foreign_key_check").fetchall(), [])
                self.assertEqual(
                    cold_restored.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='proof_marker'").fetchone(),
                    (0,),
                )
            run(["sh", str(ROOT / "scripts" / "restore-compose.sh"), str(hot_archive)], timeout=300)
            run(["sh", str(ROOT / "scripts" / "restore-compose.sh"), str(hot_archive)], timeout=300)
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
            recovered_database = copy_restored_database("recovered-snapshot", stop_service=True)
            with closing(sqlite3.connect(recovered_database)) as restored_db:
                marker = restored_db.execute("SELECT value FROM proof_marker").fetchone()
            self.assertEqual(marker, ("hot-wal-restore-proof",))
            with closing(sqlite3.connect(recovered_database)) as restored_db:
                restored_db.execute("PRAGMA foreign_keys=ON")
                version = restored_db.execute("SELECT MAX(version) FROM schema_migrations").fetchone()
                self.assertEqual(version, source_schema_version)
                self.assertEqual(restored_db.execute("PRAGMA integrity_check").fetchone(), ("ok",))
                self.assertEqual(restored_db.execute("PRAGMA foreign_key_check").fetchall(), [])
                self.assertEqual(read_menu_recovery_state(restored_db, menu_ids), hot_menu_expected)
                self.assertEqual(
                    restored_db.execute(
                        "SELECT COUNT(*) FROM pragma_foreign_key_list('meal_menu_entries') WHERE \"table\"='recipes'"
                    ).fetchone(),
                    (0,),
                )
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
                snapshots = restored_db.execute(
                    """SELECT b.id,p.state,p.removed_version_id,p.moderated_by,p.moderated_at,
                              p.moderation_reason,p.moderation_outcome,v.provenance_state,
                              v.object_uri,v.source_origin,v.source_recipe_id,v.creator_name,v.received_at,
                              v.sequence,v.event_id,v.event_type,v.payload_json,v.payload_sha256,
                              v.trusted_receipt_id,v.grant_purpose,v.grant_retention,v.grant_portability,
                              v.event_body,v.event_body_sha256,v.signature_input,v.signature_value,
                              v.request_method,v.request_target,v.covered_components,v.covered_values,
                              v.content_digest,v.authenticated_origin,v.key_id,v.public_key,
                              v.key_status_at_receipt,v.key_created_at,v.profile_at_receipt,v.accepted_at,
                              v.evidence_bytes,v.encoded_capture_bytes,v.title,v.description,v.tags_json,v.untrusted_claim
                       FROM recipe_bookmarks b JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id
                       LEFT JOIN library_remote_snapshot_versions v ON v.version_id=p.version_id
                       WHERE b.id LIKE 'snapshot-%' ORDER BY b.id"""
                ).fetchall()
                columns = ("id", "state", "removed_version_id", "moderated_by", "moderated_at", "moderation_reason",
                           "moderation_outcome", "provenance_state", "object_uri", "source_origin", "source_recipe_id",
                           "creator_name", "received_at", "sequence", "event_id", "event_type", "payload_json",
                           "payload_sha256", "trusted_receipt_id", "grant_purpose", "grant_retention", "grant_portability",
                           "event_body", "event_body_sha256", "signature_input", "signature_value", "request_method",
                           "request_target", "covered_components", "covered_values", "content_digest", "authenticated_origin",
                           "key_id", "public_key", "key_status_at_receipt", "key_created_at", "profile_at_receipt",
                           "accepted_at", "evidence_bytes", "encoded_capture_bytes", "title", "description", "tags_json",
                           "untrusted_claim")
                persisted = [dict(zip(columns, row)) for row in snapshots]
                verified_evidence = {
                    "trusted_receipt_id": b"fixture-receipt", "grant_purpose": "personal_private_retention",
                    "grant_retention": "continues_after_ordinary_withdrawal", "grant_portability": "same_person_personal_account_only",
                    "event_body": snapshot_event, "event_body_sha256": snapshot_event_hash,
                    "signature_input": "fixture signature input", "signature_value": "fixture signature",
                    "request_method": "POST", "request_target": "/api/federation/inbox",
                    "covered_components": "@method @target-uri", "covered_values": "fixture covered values",
                    "content_digest": "sha-256=:fixture:", "authenticated_origin": "https://peer.example.org",
                    "key_id": "fixture-key", "public_key": bytes(range(32)), "key_status_at_receipt": "active",
                    "key_created_at": "2026-11-01T00:00:00Z", "profile_at_receipt": "exchange-v2-retention",
                    "accepted_at": "2026-11-04T00:00:00Z", "evidence_bytes": len(snapshot_event) + 128,
                }
                for index, state in ((0, "active"), (3, "restricted")):
                    expected = {
                        "id": "snapshot-" + state, "state": state, "removed_version_id": None,
                        "moderated_by": "fixture-admin" if state == "restricted" else None,
                        "moderated_at": "2026-11-04T00:00:00Z" if state == "restricted" else None,
                        "moderation_reason": "policy" if state == "restricted" else None,
                        "moderation_outcome": "restricted" if state == "restricted" else None,
                        "provenance_state": "publisher_verified",
                        "object_uri": "https://peer.example.org/federation/recipes/" + state,
                        "source_origin": "https://peer.example.org", "source_recipe_id": state,
                        "creator_name": "Recovery fixture", "received_at": "2026-11-04T00:00:00Z",
                        "sequence": 17, "event_id": "fixture-event-17", "event_type": "recipe.snapshot",
                        "payload_json": snapshot_payload, "payload_sha256": snapshot_payload_hash,
                        "title": "Preserved snapshot", "description": "Recovery fixture", "tags_json": "[]",
                        "encoded_capture_bytes": len(snapshot_payload) + len(snapshot_event) + 256,
                        "untrusted_claim": None,
                    }
                    expected.update(verified_evidence)
                    self.assertEqual(persisted[index], expected, state + " capture evidence or moderation data changed")

                imported = persisted[1]
                # The synthetic imported-unverified row above deliberately seeds
                # these absent facts as SQL NULL; Core preserves absence and does
                # not invent source sequence/event identity during import.
                self.assertEqual(
                    {key: imported[key] for key in ("id", "state", "removed_version_id", "moderated_by", "moderated_at",
                                                    "moderation_reason", "moderation_outcome", "provenance_state", "object_uri",
                                                    "source_origin", "source_recipe_id", "creator_name", "received_at", "sequence",
                                                    "event_id", "event_type", "payload_json", "payload_sha256", "untrusted_claim")},
                    {"id": "snapshot-imported", "state": "active", "removed_version_id": None, "moderated_by": None,
                     "moderated_at": None, "moderation_reason": None, "moderation_outcome": None,
                     "provenance_state": "user_imported_unverified",
                     "object_uri": "https://peer.example.org/federation/recipes/imported",
                     "source_origin": "https://peer.example.org", "source_recipe_id": "imported",
                     "creator_name": "Recovery fixture", "received_at": "2026-11-04T00:00:00Z",
                     "sequence": None, "event_id": None, "event_type": None,
                     "payload_json": snapshot_payload, "payload_sha256": snapshot_payload_hash, "untrusted_claim": snapshot_claim},
                )
                for key in verified_evidence:
                    self.assertIsNone(imported[key], "unverified import retained trusted field " + key)
                self.assertEqual(imported["encoded_capture_bytes"], len(snapshot_payload) + len(snapshot_event) + 256)

                removed = persisted[2]
                self.assertEqual(
                    {key: removed[key] for key in ("id", "state", "removed_version_id", "moderated_by", "moderated_at",
                                                    "moderation_reason", "moderation_outcome", "provenance_state", "object_uri",
                                                    "payload_json", "event_body", "untrusted_claim")},
                    {"id": "snapshot-removed", "state": "removed", "removed_version_id": "removed-version",
                     "moderated_by": "fixture-admin", "moderated_at": "2026-11-04T00:00:00Z",
                     "moderation_reason": "privacy", "moderation_outcome": "removed", "provenance_state": None,
                     "object_uri": None, "payload_json": None, "event_body": None, "untrusted_claim": None},
                )
                self.assertEqual(restored_db.execute("SELECT COUNT(*) FROM federation_peers WHERE origin='https://peer.example.org'").fetchone(), (0,))
                self.assertEqual(restored_db.execute("SELECT COUNT(*) FROM federation_peer_keys WHERE origin='https://peer.example.org'").fetchone(), (0,))
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
            run([engine, "compose", "up", "-d", "cookbook"])
            restart_ready = False
            for _ in range(30):
                probe = subprocess.run([engine, "compose", "exec", "-T", "cookbook", "cookbook", "instance", "recovery", "describe",
                                        "--database", "/data/cookbook.db"], cwd=ROOT, env=env,
                                       text=True, capture_output=True, timeout=10)
                if probe.returncode == 0:
                    restart_ready = True
                    self.assertEqual(json.loads(probe.stdout), expected)
                    break
                time.sleep(1)
            if not restart_ready:
                self.fail("restored database did not reopen after the closed-file comparison")
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
