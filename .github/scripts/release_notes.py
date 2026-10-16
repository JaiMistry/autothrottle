#!/usr/bin/env python3
"""Release notes from what changed since the last release.

    release_notes.py notes PREV TAG [SUMMARY]   prints the notes for TAG
    release_notes.py shipped PREV               exits 0 if anything that
                                                ships changed since PREV

What ships is the code, its modules, the Dockerfile and the CSS build.
Commits that only touch tests, CI or docs are left out of the notes, and
on their own never make a release.

Commits are sorted by their first word: Add under Added, Fix under Fixed,
the rest under Changed. Dependencies get a line per package a commit
updated, read from the versions before and after it, with who updated it
and in which pull request (or commit), since Renovate groups a week's
updates into one. A section with nothing in it is left out.
"""
import json
import os
import re
import subprocess
import sys

REPO = os.environ.get("GITHUB_REPOSITORY", "JaiMistry/autothrottle")
DEP_FILES = {"go.mod", "go.sum", "package.json", "package-lock.json", "Dockerfile"}
IMAGE_NAMES = {"golang": "Go", "node": "Node.js (CSS build only)"}


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout


def show(ref, path):
    try:
        return git("show", f"{ref}:{path}")
    except subprocess.CalledProcessError:
        return ""


def ships(path):
    if path in DEP_FILES:
        return True
    if not path.startswith(("cmd/", "internal/")):
        return False
    return not path.endswith("_test.go") and "/testdata/" not in path


def commits(prev):
    out = git("log", "--no-merges", "--reverse", "--format=%H%x1f%an%x1f%s", f"{prev}..HEAD")
    for line in out.splitlines():
        sha, author, subject = line.split("\x1f")
        files = git("diff-tree", "--no-commit-id", "--name-only", "-r", sha).split()
        if any(ships(f) for f in files):
            yield sha, author, subject, files


def go_requires(ref):
    mods, block = {}, False
    for line in show(ref, "go.mod").splitlines():
        line = line.strip()
        if line.startswith("require ("):
            block = True
            continue
        if block and line == ")":
            block = False
            continue
        if "// indirect" in line:
            continue
        parts = line.removeprefix("require ").split() if block or line.startswith("require ") else []
        if len(parts) >= 2:
            mods[parts[0]] = parts[1]
    return mods


def images(ref):
    found = {}
    dockerfile = show(ref, "Dockerfile")
    # FROM source builds on an earlier stage, and FROM scratch on nothing.
    stages = {"scratch", *re.findall(r"^FROM\s.*\sAS\s+(\S+)", dockerfile, re.M | re.I)}
    # Past any flags: FROM --platform=$BUILDPLATFORM golang:1.27-alpine.
    for m in re.finditer(r"^FROM\s+(?:--\S+\s+)*(\S+)", dockerfile, re.M):
        name, _, tag = m.group(1).partition(":")
        if name in stages:
            continue
        found[IMAGE_NAMES.get(name, name)] = re.sub(r"-(alpine|slim)$", "", tag) or "latest"
    return found


def npm_direct(ref):
    try:
        pkg = json.loads(show(ref, "package.json") or "{}")
        lock = json.loads(show(ref, "package-lock.json") or "{}")
    except json.JSONDecodeError:
        return {}
    names = {**pkg.get("dependencies", {}), **pkg.get("devDependencies", {})}
    return {n: lock.get("packages", {}).get(f"node_modules/{n}", {}).get("version", "") for n in names}


def version_changes(old_ref, new_ref):
    lines = []
    for read in (images, go_requires, npm_direct):
        old, new = read(old_ref), read(new_ref)
        for name in sorted(old.keys() | new.keys(), key=lambda n: (n not in IMAGE_NAMES.values(), n)):
            a, b = old.get(name), new.get(name)
            if a == b:
                continue
            if not a:
                lines.append(f"Add {name} {b}")
            elif not b:
                lines.append(f"Remove {name}")
            else:
                lines.append(f"Update {name} to {b}")
    return lines


def credit(sha, author, subject):
    """by @author in #12, or in the commit when it didn't come from a pull request."""
    pr = re.search(r"\(#(\d+)\)$", subject)
    return f"by @{author} in " + (f"#{pr.group(1)}" if pr else sha[:7])


def notes(prev, tag, summary=""):
    out = [summary.strip(), ""] if summary.strip() else []
    if not prev:
        return "\n".join(out or ["Initial release."]).strip() + "\n"

    sections = {"Added": [], "Changed": [], "Fixed": [], "Dependencies": []}
    for sha, author, subject, files in commits(prev):
        word = subject.split()[0]
        update = author.startswith("renovate") or (word in ("Update", "Bump") and all(f in DEP_FILES for f in files))
        if any(f in DEP_FILES for f in files):
            changes = version_changes(f"{sha}^", sha)
            if not changes and update:
                # Something the comparison doesn't read, such as a package
                # only the CSS build's tools use.
                changes = [re.sub(r"\s*\(#\d+\)$", "", subject)]
            sections["Dependencies"] += [f"{c} {credit(sha, author, subject)}" for c in changes]
        if update:
            continue
        sections["Added" if word == "Add" else "Fixed" if word == "Fix" else "Changed"].append(subject)

    if any(sections.values()):
        out += ["## What's changed", ""]
        for title, items in sections.items():
            if items:
                out += [f"### {title}", *[f"- {i}" for i in items], ""]
    out.append(f"**Full Changelog**: https://github.com/{REPO}/compare/{prev}...{tag}")
    return "\n".join(out) + "\n"


if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    if cmd == "notes":
        sys.stdout.write(notes(args[0], args[1], args[2] if len(args) > 2 else ""))
    elif cmd == "shipped":
        sys.exit(0 if next(commits(args[0]), None) else 1)
    else:
        sys.exit(f"unknown command: {cmd}")
