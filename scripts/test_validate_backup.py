import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from validate_backup import BackupValidationError, validate_backup


def create_archive(entries):
    temporary = tempfile.NamedTemporaryFile(suffix=".tar.gz", delete=False)
    temporary.close()
    path = Path(temporary.name)
    with tarfile.open(path, mode="w:gz") as archive:
        for name, kind, value in entries:
            info = tarfile.TarInfo(name)
            if kind == "directory":
                info.type = tarfile.DIRTYPE
                archive.addfile(info)
            elif kind == "symlink":
                info.type = tarfile.SYMTYPE
                info.linkname = value
                archive.addfile(info)
            else:
                data = value.encode()
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
    return path


class BackupValidationTests(unittest.TestCase):
    def setUp(self):
        self.valid_entries = [
            ("cookbook.db", "file", "sqlite snapshot"),
            ("media", "directory", ""),
            ("media/0123456789abcdef0123456789abcdef.png", "file", "image"),
        ]
        self.paths = []

    def tearDown(self):
        for path in self.paths:
            path.unlink(missing_ok=True)

    def archive(self, entries):
        path = create_archive(entries)
        self.paths.append(path)
        return path

    def test_accepts_database_sidecars_and_flat_media(self):
        entries = self.valid_entries + [
            ("cookbook.db-wal", "file", "wal"),
            ("cookbook.db-journal", "file", "journal"),
            ("media/abcdef0123456789abcdef0123456789.webp", "file", "image"),
        ]
        validate_backup(self.archive(entries))

    def test_rejects_parent_path_traversal(self):
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive(self.valid_entries + [("../escape", "file", "x")]))
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive(self.valid_entries + [("media//0123456789abcdef0123456789abcdef.png", "file", "x")]))

    def test_rejects_unexpected_nested_paths(self):
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive(self.valid_entries + [("media/nested/image.png", "file", "x")]))

    def test_rejects_symbolic_links(self):
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive(self.valid_entries + [("media/abcdef0123456789abcdef0123456789.jpg", "symlink", "/etc/passwd")]))

    def test_rejects_duplicate_and_invalid_media_names(self):
        duplicate = self.valid_entries + [("cookbook.db", "file", "second database")]
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive(duplicate))
        invalid_media = self.valid_entries + [("media/secret.txt", "file", "private")]
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive(invalid_media))

    def test_rejects_media_larger_than_the_upload_limit(self):
        oversized = self.valid_entries + [
            ("media/abcdef0123456789abcdef0123456789.jpg", "file", "x" * ((5 << 20) + 1))
        ]
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive(oversized))

    def test_requires_database_and_media_directory(self):
        with self.assertRaises(BackupValidationError):
            validate_backup(self.archive([("media", "directory", "")]))


if __name__ == "__main__":
    unittest.main()
