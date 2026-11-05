"""Files in and out of a machine: streamed with their length, read whole or into a file."""

from __future__ import annotations

import io
import os
import shutil
import tempfile
import unittest
from pathlib import Path

from support import APITest

from wheelmachine import BadRequestError, NotFoundError, UnavailableError


class NoSeek(io.RawIOBase):
    """A stream that cannot seek, such as a pipe."""

    def __init__(self, data: bytes) -> None:
        self._data = io.BytesIO(data)

    def readable(self) -> bool:
        return True

    def seekable(self) -> bool:
        return False

    def read(self, n: int = -1) -> bytes:
        return self._data.read(n)


class Files(APITest):
    def setUp(self) -> None:
        super().setUp()
        self.machine = self.client.machines.create()
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def test_bytes_go_in_and_come_back(self) -> None:
        self.client.machines.write_file(self.machine.id, "work/notes.txt", b"hello\n")
        seen = self.api.requests("PUT", "/files$")[-1]
        self.assertEqual(seen.query, {"path": ["work/notes.txt"]})
        self.assertEqual(seen.headers["content-length"], "6")
        self.assertEqual(seen.headers["content-type"], "application/octet-stream")
        self.assertNotIn("transfer-encoding", seen.headers)
        self.assertEqual(self.client.machines.read_file(self.machine.id, "work/notes.txt"), b"hello\n")

    def test_a_path_streams_from_disk_with_its_length(self) -> None:
        data = os.urandom(300_000)
        local = self.tmp / "data.bin"
        local.write_bytes(data)
        self.client.machines.write_file(self.machine.id, "home/data.bin", local)
        seen = self.api.requests("PUT", "/files$")[-1]
        self.assertEqual(seen.headers["content-length"], str(len(data)))
        self.assertNotIn("transfer-encoding", seen.headers)
        self.assertEqual(self.api.files[(self.machine.id, "home/data.bin")], data)

    def test_a_file_object_streams_from_where_it_is(self) -> None:
        data = os.urandom(200_000)
        local = self.tmp / "data.bin"
        local.write_bytes(data)
        with open(local, "rb") as f:
            f.seek(1000)
            self.client.machines.write_file(self.machine.id, "work/tail.bin", f)
        self.assertEqual(self.api.requests("PUT", "/files$")[-1].headers["content-length"], str(len(data) - 1000))
        self.assertEqual(self.api.files[(self.machine.id, "work/tail.bin")], data[1000:])

    def test_a_stream_that_cannot_seek_needs_its_length(self) -> None:
        with self.assertRaises(ValueError):
            self.client.machines.write_file(self.machine.id, "work/x", NoSeek(b"abc"))  # type: ignore[arg-type]
        self.client.machines.write_file(self.machine.id, "work/x", NoSeek(b"abc"), content_length=3)  # type: ignore[arg-type]
        self.assertEqual(self.api.files[(self.machine.id, "work/x")], b"abc")

    def test_a_seekable_file_is_sent_again_after_a_refusal(self) -> None:
        self.api.fault("PUT", "/files$", status=503, code="unavailable", retry_after=1)
        self.client.machines.write_file(self.machine.id, "work/y", io.BytesIO(b"again"))
        self.assertEqual(self.api.files[(self.machine.id, "work/y")], b"again")
        self.assertEqual(self.sleeps, [1.0])

    def test_a_stream_already_read_is_not_sent_again(self) -> None:
        self.api.fault("PUT", "/files$", status=503, code="unavailable", retry_after=1)
        with self.assertRaises(UnavailableError):
            self.client.machines.write_file(self.machine.id, "work/z", NoSeek(b"once"), content_length=4)  # type: ignore[arg-type]
        self.assertEqual(self.sleeps, [])

    def test_text_and_other_things_are_refused(self) -> None:
        with self.assertRaises(TypeError):
            self.client.machines.write_file(self.machine.id, "work/x", "some text")  # type: ignore[arg-type]
        with self.assertRaises(TypeError):
            self.client.machines.write_file(self.machine.id, "work/x", 12)  # type: ignore[arg-type]
        local = self.tmp / "t.txt"
        local.write_text("text")
        with open(local) as f, self.assertRaises(TypeError):
            self.client.machines.write_file(self.machine.id, "work/x", f)  # type: ignore[arg-type]
        with self.assertRaises(FileNotFoundError):
            self.client.machines.write_file(self.machine.id, "work/x", self.tmp / "missing")
        self.assertEqual(self.api.requests("PUT", "/files$"), [])

    def test_a_length_that_is_not_the_datas(self) -> None:
        with self.assertRaises(ValueError):
            self.client.machines.write_file(self.machine.id, "work/x", b"abc", content_length=5)

    def test_reading_into_a_path_writes_it_whole(self) -> None:
        data = os.urandom(500_000)
        self.api.files[(self.machine.id, "work/out.bin")] = data
        dest = self.tmp / "out.bin"
        n = self.client.machines.read_file(self.machine.id, "work/out.bin", to=dest)
        self.assertEqual(n, len(data))
        self.assertEqual(dest.read_bytes(), data)
        self.assertEqual(sorted(p.name for p in self.tmp.iterdir()), ["out.bin"])
        self.assertEqual(self.api.seen[-1].headers["accept"], "application/octet-stream")

    def test_a_file_that_is_not_there_leaves_nothing_behind(self) -> None:
        dest = self.tmp / "none.bin"
        with self.assertRaises(NotFoundError) as caught:
            self.client.machines.read_file(self.machine.id, "work/none", to=str(dest))
        self.assertEqual(caught.exception.detail, "no such file in the machine")
        self.assertEqual(list(self.tmp.iterdir()), [])

    def test_reading_into_a_file_object(self) -> None:
        self.api.files[(self.machine.id, "home/a")] = b"abc" * 1000
        out = io.BytesIO()
        self.assertEqual(self.client.machines.read_file(self.machine.id, "home/a", to=out), 3000)
        self.assertEqual(out.getvalue(), b"abc" * 1000)

    def test_a_path_outside_the_two_folders_is_refused(self) -> None:
        with self.assertRaises(BadRequestError):
            self.client.machines.read_file(self.machine.id, "etc/passwd")

    def test_a_refusal_before_the_body_is_the_answer(self) -> None:
        # the API refuses a write to a machine that is not there before it reads the body, and closes
        with self.assertRaises(NotFoundError):
            self.client.machines.write_file("m_" + "7" * 26, "work/big", os.urandom(8 << 20))


if __name__ == "__main__":
    unittest.main()
