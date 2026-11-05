"""Team folders: the folders you see, and a file of one put, listed, read, moved and deleted to the Trash."""

from __future__ import annotations

import unittest

from support import APITest


class Folders(APITest):
    def test_a_file_put_listed_read_moved_and_deleted(self) -> None:
        folders = self.client.folders()
        self.assertTrue(folders.on)
        self.assertEqual([(f.id, f.name, f.access) for f in folders.data], [("fld_own", "My files", "edit")])
        e = self.client.write_folder_file("fld_own", "notes/plan.md", b"the plan")
        self.assertEqual((e.name, e.size, e.changed_by), ("plan.md", 8, "Ada"))
        self.assertEqual(self.api.requests("PUT", "/v1/folders/fld_own/files")[-1].query["path"], ["notes/plan.md"])
        self.assertEqual([x.name for x in self.client.list_folder("fld_own").entries], ["notes/plan.md"])
        # a page at a time: each page asked for from the `next` of the one before
        self.client.write_folder_file("fld_own", "notes/zz.md", b"z")
        first = self.client.list_folder("fld_own", limit=1)
        self.assertEqual(([x.name for x in first.entries], first.next), (["notes/plan.md"], "1"))
        self.assertEqual([x.name for x in self.client.folder_entries("fld_own", limit=1)], ["notes/plan.md", "notes/zz.md"])
        self.assertEqual(self.api.requests("GET", "/v1/folders/fld_own/list")[-1].query["after"], ["1"])
        self.client.delete_folder_file("fld_own", "notes/zz.md")
        self.assertEqual(self.client.read_folder_file("fld_own", "notes/plan.md"), b"the plan")
        self.client.move_folder_item("fld_own", "notes/plan.md", "plan.md")
        t = self.client.delete_folder_file("fld_own", "plan.md")
        self.assertEqual((t.path, t.goes_at), ("plan.md", "2026-12-04T10:00:00Z"))
        self.assertEqual(self.client.folders().data[0].bytes, 0)


if __name__ == "__main__":
    unittest.main()
