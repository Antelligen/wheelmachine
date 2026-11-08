"""Deploy: a deployment made from each kind of source, a folder packed and sent without what stays here, a run followed,
and what else a deployment can be asked."""

from __future__ import annotations

import io
import json
import os
import tarfile
import tempfile
import unittest
from pathlib import Path
from typing import Any, Dict, Sequence
from unittest import mock

from support import APITest

from wheelmachine import (
    ConflictError,
    Deployment,
    DeployGuess,
    DeployLog,
    DeployRepo,
    DeployRun,
    NotFoundError,
    WaitTimeoutError,
    WheelmachineError,
)
from wheelmachine._resources import _ignored_by, _parse_ignore


class Deployments(APITest):
    def folder(self) -> Path:
        made = tempfile.TemporaryDirectory()
        self.addCleanup(made.cleanup)
        root = Path(made.name) / "shop"
        for name, text in {"package.json": '{"name":"x"}', "run.sh": "#!/bin/sh\n", "lib/a.js": "a", ".env": "S=1", "lib/.env.local": "S=2",
                           ".git/config": "x", "node_modules/m/i.js": "x", "lib/__pycache__/a.pyc": "x", "environment.js": "kept"}.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
        os.chmod(root / "run.sh", 0o755)
        os.symlink("run.sh", root / "link")
        return root

    def test_a_repository_of_a_connection_is_deployed_and_followed(self) -> None:
        d = self.client.deployments.create(connection="code_1", repo="acme/shop", start="npm start", port=3000, build="npm ci", name="shop", branch="main",
                                           cpus=4, region="helsinki", secrets=["API_KEY"], keep_awake=True)
        seen = self.api.requests("POST", "/v1/deployments$")[-1]
        self.assertEqual(seen.json(), {"name": "shop", "source": {"connection": "code_1", "repo": "acme/shop"}, "branch": "main", "build": "npm ci",
                                       "start": "npm start", "port": 3000, "cpus": 4, "region": "helsinki", "secrets": ["API_KEY"], "keep_awake": True})
        self.assertIn("idempotency-key", seen.headers)
        self.assertIsInstance(d, Deployment)
        self.assertEqual((d.state, d.source.kind, d.source.repo, d.keep_awake), ("building", "repo", "acme/shop", True))
        self.assertIsInstance(d.run, DeployRun)
        self.assertEqual(d.last_response.status if d.last_response else None, 201)
        live = self.client.deployments.wait(d.id)
        self.assertEqual((live.state, live.run.state if live.run else None), ("live", "live"))
        self.assertEqual(live.url, "https://3000-{}.run.wheelmachine.ai".format(d.machine[2:] if d.machine else ""))

    def test_a_public_address_is_deployed(self) -> None:
        d = self.client.deployments.create(url="https://github.com/acme/shop", start="go run .", port=8080)
        self.assertEqual(self.api.requests("POST", "/v1/deployments$")[-1].json()["source"], {"url": "https://github.com/acme/shop"})
        self.assertEqual(d.source.kind, "url")

    def test_a_folder_is_sent_without_what_stays_here(self) -> None:
        d = self.client.deployments.create(upload=True, start="npm start", port=3000)
        self.assertEqual(d.state, "waiting_for_files")
        sent = self.client.deployments.send_folder(d.id, self.folder())
        self.assertEqual(sent.state, "building")
        self.assertEqual(sent.left_out, [".env", ".git/", "lib/.env.local", "lib/__pycache__/", "link", "node_modules/"])
        files = self.api.deploy_sent[d.id]
        self.assertEqual(sorted(files), ["environment.js", "lib", "lib/a.js", "package.json", "run.sh"])
        self.assertEqual(files["lib/a.js"][0], "a")
        self.assertEqual((files["run.sh"][1], files["lib/a.js"][1]), (0o755, 0o644))
        seen = self.api.requests("PUT", "/files$")[-1]
        self.assertEqual(int(seen.headers["content-length"]), len(seen.body))

    def test_a_folder_with_nothing_to_send_is_told_before_anything_is_sent(self) -> None:
        d = self.client.deployments.create(upload=True, start="x", port=1)
        with tempfile.TemporaryDirectory() as empty:
            (Path(empty) / ".env").write_text("S=1")
            with self.assertRaisesRegex(ValueError, "no files"):
                self.client.deployments.send_folder(d.id, empty)
            with self.assertRaisesRegex(ValueError, "is not a folder"):
                self.client.deployments.send_folder(d.id, Path(empty) / "no-such")
        self.assertEqual(self.api.requests("PUT", "/files$"), [])

    def tree(self, files: Dict[str, str]) -> Path:
        made = tempfile.TemporaryDirectory()
        self.addCleanup(made.cleanup)
        root = Path(os.path.realpath(made.name)) / "app"
        for name, text in files.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
        return root

    def cases(self) -> Dict[str, Any]:
        with open(Path(__file__).parent / "deploy_left_out.json", encoding="utf-8") as f:
            return json.load(f)

    def shared_tree(self, c: Dict[str, Any], more: Sequence[str] = ()) -> Path:
        return self.tree({**{name: "x" for name in [*c["files"], *more]}, **c["gitignores"]})

    def test_the_cases_every_packer_shares_hold(self) -> None:
        c = self.cases()
        found = self.client.deployments.folder_files(self.shared_tree(c))
        self.assertEqual(found.files, c["goes"])
        self.assertEqual(found.left_out, c["left_out"])
        ex = c["exclude"]
        found = self.client.deployments.folder_files(self.shared_tree(c, ex["files"]), exclude=ex["patterns"])
        self.assertEqual(found.files, sorted(set(c["goes"] + ex["goes"]) - set(ex["left_out"])))
        self.assertEqual(found.left_out, sorted(c["left_out"] + ex["left_out"]))
        found = self.client.deployments.folder_files(self.shared_tree(c), gitignore=False)
        self.assertEqual(found.files, sorted(c["goes"] + c["no_gitignore"]["goes"]))
        self.assertEqual(self.api.requests("GET", "."), [])

    def test_a_gitignore_line_matches_as_it_does_in_every_packer(self) -> None:
        for pattern, path, is_dir, want in self.cases()["patterns"]:
            self.assertEqual(_ignored_by(_parse_ignore("", pattern), path, is_dir), want, (pattern, path, is_dir))

    def test_what_goes_is_sent_and_nothing_else(self) -> None:
        root = self.tree({"index.html": "hi", ".npmrc": "token", ".aws/credentials": "k", "id_rsa": "k", "secrets.json": "s", "a.pem": "k", ".git": "gitdir: /x", ".gitignore": "named.txt\n", "named.txt": "n",
                          ".config/wheelmachine/config.json": "{}", "skip.me": "x"})
        d = self.client.deployments.create(upload=True, start="x", port=1)
        sent = self.client.deployments.send_folder(d.id, root, exclude=["*.me"])
        self.assertEqual(sorted(self.api.deploy_sent[d.id]), [".gitignore", "index.html"])
        self.assertEqual(sent.left_out, [".aws/", ".config/", ".git", ".npmrc", "a.pem", "id_rsa", "named.txt", "secrets.json", "skip.me"])

    def test_a_folder_named_by_a_link_is_that_folder(self) -> None:
        root = self.tree({"index.html": "hi"})
        link = root.parent / "link"
        os.symlink(root, link)
        self.assertEqual(self.client.deployments.folder_files(link).files, ["index.html"])

    def test_detect_follows_no_link_out_of_the_folder(self) -> None:
        planted = self.tree({"environ": "web: WHEELMACHINE_API_KEY=planted"}) / "environ"
        root = self.tree({"package.json": '{"name":"x"}', "go.mod": "module x", ".gitignore": "go.mod\n"})
        for name in ("Procfile", "requirements.txt", "index.html"):
            os.symlink(planted, root / name)
        self.client.deployments.detect(folder=root)
        self.assertEqual(self.api.requests("POST", "/detect$")[-1].json(), {"files": {"package.json": '{"name":"x"}'}})
        self.client.deployments.detect(folder=root, gitignore=False)
        self.assertEqual(self.api.requests("POST", "/detect$")[-1].json(), {"files": {"package.json": '{"name":"x"}', "go.mod": "module x"}})
        self.client.deployments.detect(folder=root, exclude=["package.json"])
        self.assertEqual(self.api.requests("POST", "/detect$")[-1].json(), {"files": {}})

    def test_a_folder_over_what_the_api_takes_is_told_first(self) -> None:
        d = self.client.deployments.create(upload=True, start="x", port=1)
        for says, files in {"at most 255 bytes": {"/".join(["a" * 30] * 9) + "/c": "x"}, "folders deep": {"a/" * 33 + "c": "x"}, "no control characters": {"a\tb": "x"}}.items():
            with self.assertRaisesRegex(ValueError, says):
                self.client.deployments.send_folder(d.id, self.tree(files))
        root = self.tree({"a": "x", "b": "x"})
        with mock.patch("wheelmachine._resources._DEPLOY_FILES_MOST", 1), self.assertRaisesRegex(ValueError, "at most 1 files"):
            self.client.deployments.send_folder(d.id, root)
        for name, size, says in (("big", 100_000_001, "a file is at most 100 MB"), ):
            with open(root / name, "wb") as f:
                f.truncate(size)
            with self.assertRaisesRegex(ValueError, says):
                self.client.deployments.send_folder(d.id, root)
            os.remove(root / name)
        for i in range(6):
            with open(root / "part{}".format(i), "wb") as f:
                f.truncate(99_000_000)
        with self.assertRaisesRegex(ValueError, "at most 500 MB"):
            self.client.deployments.folder_files(root)
        self.assertEqual(self.api.requests("PUT", "/files$"), [])

    def test_a_folder_that_cannot_be_read_is_said_not_left_behind(self) -> None:
        if os.geteuid() == 0:
            self.skipTest("root reads everything")
        root = self.tree({"a": "x", "shut/b": "x"})
        os.chmod(root / "shut", 0)
        self.addCleanup(os.chmod, root / "shut", 0o755)
        with self.assertRaises(PermissionError):
            self.client.deployments.folder_files(root)

    def test_a_home_folder_and_a_folder_of_settings_are_refused(self) -> None:
        home = self.tree({".ssh/id_rsa": "k", "shop/index.html": "hi", ".config/wheelmachine/config.json": "{}", "code/.aws/x/f": "k"})
        conf = self.tree({"wheelmachine/config.json": "{}", "f": "x"})
        with mock.patch.dict(os.environ, {"HOME": str(home)}):
            os.environ.pop("XDG_CONFIG_HOME", None)
            for folder in (home, home.parent, Path("/"), home / ".ssh", home / ".config", home / ".config" / "wheelmachine", home / "code" / ".aws" / "x"):
                with self.assertRaises(ValueError, msg=str(folder)):
                    self.client.deployments.folder_files(folder)
                with self.assertRaises(ValueError, msg=str(folder)):
                    self.client.deployments.detect(folder=folder)
            self.assertEqual(self.client.deployments.folder_files(home / "shop").files, ["index.html"])
            with mock.patch.dict(os.environ, {"XDG_CONFIG_HOME": str(conf)}):
                for folder in (conf, conf / "wheelmachine"):
                    with self.assertRaisesRegex(ValueError, "settings"):
                        self.client.deployments.folder_files(folder)
                self.assertEqual(self.client.deployments.folder_files(home / "shop").files, ["index.html"])

    def test_files_wait_for_their_machine(self) -> None:
        self.api.deploy_files_after = 3
        with mock.patch("wheelmachine._resources._DEPLOY_FILES_POLL_S", 0.001):
            d = self.client.deployments.create(upload=True, start="x", port=1)
            sent = self.client.deployments.send_folder(d.id, self.tree({"index.html": "hi"}))
            self.assertEqual(sent.state, "building")
            self.assertEqual(sorted(self.api.deploy_sent[d.id]), ["index.html"])
            self.api.deploy_files_after = 1 << 30
            d = self.client.deployments.create(upload=True, start="x", port=1)
            with self.assertRaisesRegex(WaitTimeoutError, d.id):
                self.client.deployments.send_folder(d.id, self.tree({"index.html": "hi"}), ready_timeout_s=0.01)
            self.assertNotIn(d.id, self.api.deploy_sent)

    def test_an_archive_is_sent_as_it_is(self) -> None:
        d = self.client.deployments.create(upload=True, start="x", port=1)
        packed = io.BytesIO()
        with tarfile.open(fileobj=packed, mode="w") as archive:
            info = tarfile.TarInfo("index.html")
            info.size = 2
            archive.addfile(info, io.BytesIO(b"hi"))
        self.client.deployments.send_files(d.id, packed.getvalue())
        self.assertEqual(self.api.deploy_sent[d.id]["index.html"][0], "hi")

    def test_a_redeploy_is_never_sent_twice_and_one_at_a_time(self) -> None:
        d = self.client.deployments.create(url="https://github.com/acme/shop", start="x", port=1)
        self.client.deployments.wait(d.id)
        again = self.client.deployments.redeploy(d.id)
        self.assertEqual(again.run.why if again.run else None, "redeploy")
        self.assertEqual(again.last_response.status if again.last_response else None, 202)
        with self.assertRaises(ConflictError):
            self.client.deployments.redeploy(d.id)
        self.assertEqual(len(self.api.requests("POST", "/redeploy$")), 2)

    def test_a_wait_too_long_says_so(self) -> None:
        d = self.client.deployments.create(url="https://github.com/acme/shop", start="x", port=1)
        self.api.deploy_stuck = True
        with self.assertRaises(WaitTimeoutError):
            self.client.deployments.wait(d.id, timeout_s=1)
        self.assertEqual(self.api.requests("GET", "/wait$")[-1].query.get("timeout_s"), ["1"])

    def test_it_is_listed_read_changed_and_deleted(self) -> None:
        a = self.client.deployments.create(url="https://github.com/acme/a", start="x", port=1, name="a", project="prj_a")
        b = self.client.deployments.create(url="https://github.com/acme/b", start="x", port=1, name="b")
        self.assertEqual([d.name for d in self.client.deployments.list()], ["b", "a"])
        self.assertEqual([d.id for d in self.client.deployments.list(project="prj_a")], [a.id])
        self.assertEqual(self.client.deployments.get(b.id).name, "b")
        changed = self.client.deployments.update(b.id, port=9000, keep_awake=True)
        self.assertEqual(self.api.requests("PATCH", "/v1/deployments/")[-1].json(), {"port": 9000, "keep_awake": True})
        self.assertEqual((changed.port, changed.keep_awake), (9000, True))
        runs = self.client.deployments.runs(b.id)
        self.assertEqual([r.why for r in runs], ["first"])
        log = self.client.deployments.logs(b.id, kind="app")
        self.assertIsInstance(log, DeployLog)
        self.assertEqual((log.kind, log.text, log.done), ("app", "built", True))
        self.assertEqual(self.api.requests("GET", "/logs$")[-1].query.get("kind"), ["app"])
        self.client.deployments.delete(b.id)
        with self.assertRaises(NotFoundError):
            self.client.deployments.get(b.id)

    def test_how_it_builds_is_asked_of_a_repository_or_a_folder(self) -> None:
        guess = self.client.deployments.detect(connection="code_1", repo="acme/shop", branch="main")
        self.assertEqual(self.api.requests("POST", "/detect$")[-1].json(), {"source": {"connection": "code_1", "repo": "acme/shop"}, "branch": "main"})
        self.assertIsInstance(guess, DeployGuess)
        self.assertEqual((guess.runtime, guess.start, guess.port), ("node", "npm start", 3000))
        root = self.folder()
        (root / "yarn.lock").write_text("long")
        (root / "README.md").write_text("not read")
        self.client.deployments.detect(folder=root)
        self.assertEqual(self.api.requests("POST", "/detect$")[-1].json(), {"files": {"package.json": '{"name":"x"}', "yarn.lock": None}})

    def test_what_can_be_deployed_is_listed(self) -> None:
        self.api.deploy_repos = [{"object": "deploy_repo", "connection": "code_1", "provider": "github", "host": "github.com", "repo": "acme/shop", "shared": True, "updated_at": None}]
        repos = self.client.deployments.repos(connection="code_1")
        self.assertIsInstance(repos[0], DeployRepo)
        self.assertEqual((repos[0].repo, repos[0].shared), ("acme/shop", True))
        self.assertEqual(self.client.deployments.repos(connection="code_2"), [])
        branches = self.client.deployments.branches(connection="code_1", repo="acme/shop")
        self.assertEqual((branches.default, branches.known, [b.name for b in branches.data]), ("main", True, ["main"]))

    def test_until_deploy_opens_each_call_says_so(self) -> None:
        self.api.deploy_closed = True
        with self.assertRaises(WheelmachineError) as told:
            self.client.deployments.list()
        self.assertEqual(getattr(told.exception, "code", None), "not_open")
        self.assertIn("Not open yet", self.client.deployments.__doc__ or "")


if __name__ == "__main__":
    unittest.main()
