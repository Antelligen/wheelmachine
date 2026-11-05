#!/usr/bin/env python3
"""wm and wm-mcp, built for a release: for Linux, macOS and Windows, on amd64 and arm64, an archive each (tar.gz, and
zip for Windows) holding both programs with their licences, and one SHA256SUMS for them all.

The programs are built with plain go build: no C (CGO_ENABLED=0), no paths of the computer that built them
(-trimpath), no stamp of the checkout (-buildvcs=false), and the version - api.Version, the one place it is kept -
stamped in with -ldflags -X. Built again from the same source with the same Go, the archives are the same byte for
byte: files in name order, owned by nobody, dated SOURCE_DATE_EPOCH, else the time of the commit checked out.

    python3 frontend/cli/release.py <out folder> [--version <version>]

--version is the version the release's tag says (wm-v<version>): the build stops when the code says another. The out
folder must be empty or new. Needs Go (the module names the toolchain it fetches) and Python 3.9 or later.
"""
from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import zipfile
from pathlib import Path

CLI = Path(__file__).resolve().parent
TARGETS = [("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64"),
           ("windows", "amd64"), ("windows", "arm64")]
PROGRAMS = ("wm", "wm-mcp")
#: beside the programs in every archive; LICENSE-go is the licence of the Go the programs carry, from that Go itself
FILES = ("LICENSE", "NOTICE", "README.md", "CHANGELOG.md")
VERSION_VAR = "wheelmachine/frontend/cli/api.Version"
#: 1980-01-01, the earliest moment a zip can hold
ZIP_EPOCH = 315532800


def fail(why: str) -> None:
    raise SystemExit(f"release: {why}")


def source_version() -> str:
    found = re.findall(r'^var Version = "([^"]+)"$', (CLI / "api" / "client.go").read_text(encoding="utf-8"), re.M)
    if len(found) != 1:
        fail("api/client.go should hold one var Version")
    return found[0]


def go(*args: str, env: dict[str, str] | None = None) -> str:
    return subprocess.run(["go", *args], cwd=CLI, env=env, check=True, capture_output=True, text=True).stdout.strip()


def date_of_source() -> int:
    if os.environ.get("SOURCE_DATE_EPOCH"):
        return int(os.environ["SOURCE_DATE_EPOCH"])
    try:
        out = subprocess.run(["git", "-C", str(CLI), "log", "-1", "--format=%ct"], check=True, capture_output=True,
                             text=True).stdout.strip()
        return int(out)
    except (OSError, ValueError, subprocess.CalledProcessError):
        return ZIP_EPOCH


def build(goos: str, goarch: str, version: str, into: Path) -> None:
    # GOAMD64 and GOARM64 at their first levels, whatever this computer's settings: the programs run on any processor
    # of the kind, and the same source gives the same bytes
    env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch, GOAMD64="v1", GOARM64="v8.0")
    ext = ".exe" if goos == "windows" else ""
    for prog in PROGRAMS:
        subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-s -w -X {VERSION_VAR}={version}",
                        "-o", str(into / (prog + ext)), "./cmd/" + prog], cwd=CLI, env=env, check=True)


def is_program(name: str) -> bool:
    return (name[:-len(".exe")] if name.endswith(".exe") else name) in PROGRAMS


def tar_gz(folder: Path, out: Path, when: int) -> Path:
    def entry(name: str, kind: bytes, mode: int, size: int = 0) -> tarfile.TarInfo:
        info = tarfile.TarInfo(name)
        info.type, info.mode, info.size, info.mtime = kind, mode, size, when
        info.uid = info.gid = 0
        info.uname = info.gname = ""
        return info

    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        tar.addfile(entry(folder.name + "/", tarfile.DIRTYPE, 0o755))
        for f in sorted(folder.iterdir()):
            with f.open("rb") as data:
                tar.addfile(entry(f"{folder.name}/{f.name}", tarfile.REGTYPE, 0o755 if is_program(f.name) else 0o644,
                                  f.stat().st_size), data)
    path = out / (folder.name + ".tar.gz")
    with path.open("wb") as fh, gzip.GzipFile(filename="", mode="wb", fileobj=fh, mtime=0, compresslevel=9) as gz:
        gz.write(raw.getvalue())
    return path


def zip_(folder: Path, out: Path, when: int) -> Path:
    stamp = time.gmtime(max(when, ZIP_EPOCH))[:6]

    def entry(name: str, mode: int) -> zipfile.ZipInfo:
        info = zipfile.ZipInfo(name, date_time=stamp)
        info.create_system = 3  # the modes below are Unix modes, wherever the archive is made
        info.external_attr = mode << 16
        return info

    path = out / (folder.name + ".zip")
    with zipfile.ZipFile(path, "w") as z:
        d = entry(folder.name + "/", 0o40755)
        d.external_attr |= 0x10  # and a folder to Windows
        z.writestr(d, b"")
        for f in sorted(folder.iterdir()):
            info = entry(f"{folder.name}/{f.name}", 0o100755 if is_program(f.name) else 0o100644)
            info.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(info, f.read_bytes(), compresslevel=9)
    return path


def main() -> int:
    ap = argparse.ArgumentParser(description="wm and wm-mcp, built for a release")
    ap.add_argument("out", type=Path, help="an empty or new folder for the archives and SHA256SUMS")
    ap.add_argument("--version", help="the version the release's tag says; the build stops when the code says another")
    args = ap.parse_args()
    version = source_version()
    if args.version is not None and args.version != version:
        fail(f"the tag says {args.version} and the code says {version} (api/client.go): one version, said once")
    out = args.out.resolve()
    if out.exists() and any(out.iterdir()):
        fail(f"{out} is not empty")
    out.mkdir(parents=True, exist_ok=True)
    when = date_of_source()
    go_licence = Path(go("env", "GOROOT")) / "LICENSE"
    host = (go("env", "GOHOSTOS"), go("env", "GOHOSTARCH"))
    dated = time.strftime("%Y-%m-%d %H:%M:%SZ", time.gmtime(when))
    print(f"wm {version}, built with {go('env', 'GOVERSION')}, dated {dated}")

    archives = []
    with tempfile.TemporaryDirectory() as work:
        for goos, goarch in TARGETS:
            folder = Path(work) / f"wm_{version}_{goos}_{goarch}"
            folder.mkdir()
            build(goos, goarch, version, folder)
            for name in FILES:
                shutil.copyfile(CLI / name, folder / name)
            shutil.copyfile(go_licence, folder / "LICENSE-go")
            if (goos, goarch) == host:
                said = subprocess.run([str(folder / "wm"), "--version"], check=True, capture_output=True,
                                      text=True).stdout
                if said != f"wm {version}\n":
                    fail(f"the built wm says {said.strip()!r}, not wm {version}")
            archives.append(zip_(folder, out, when) if goos == "windows" else tar_gz(folder, out, when))

    sums = "".join(f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n" for p in sorted(archives))
    (out / "SHA256SUMS").write_text(sums, encoding="ascii")
    for p in sorted(out.iterdir()):
        print(f"{p.stat().st_size:>10}  {p.name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
