#!/usr/bin/env python3
"""The changelog's entry for a release, and whether anything shipped.

    release_notes.py changelog PREV TAG DATE [SUMMARY]
        moves CHANGELOG.md's Unreleased entries under TAG, released on DATE
    release_notes.py section TAG
        prints TAG's entry, as the release's notes
    release_notes.py shipped PREV
        exits 0 if anything that ships changed since PREV

CHANGELOG.md follows Keep a Changelog: each change that people would
notice adds its line under Unreleased as it lands. A release moves those
lines under its version, with a summary above them if one is given.

What ships is the code, its modules, the Dockerfile and the CSS build.
Commits that only touch tests, CI or docs never make a release on their
own. If nothing was written under Unreleased, the release lists the
commits that shipped instead, sorted by their first word: Add under
Added, Fix under Fixed, the rest under Changed.

Dependencies are always listed from the versions before and after each
commit, with who updated them and in which pull request (or commit),
since Renovate groups a week's updates into one and writes no entries.
A section with nothing in it is left out.
"""
import json
import os
import re
import subprocess
import sys

REPO = os.environ.get("GITHUB_REPOSITORY", "JaiMistry/autothrottle")
CHANGELOG = "CHANGELOG.md"
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


def compare(prev, tag):
    return f"https://github.com/{REPO}/compare/{prev}...{tag}" if prev else f"https://github.com/{REPO}/releases/tag/{tag}"


def generated(prev):
    """Added, Changed, Fixed from the commits, and Dependencies, as {title: [lines]}."""
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
    return sections


def split(text):
    """The changelog as (head, unreleased body, the rest), the rest starting at the first release."""
    m = re.search(r"^## \[Unreleased\][^\n]*\n", text, re.M)
    if not m:
        sys.exit(f"{CHANGELOG} has no ## [Unreleased] heading")
    nxt = re.search(r"^## \[|^\[[^\]]+\]: ", text[m.end():], re.M)
    end = m.end() + (nxt.start() if nxt else len(text) - m.end())
    return text[:m.end()], text[m.end():end].strip(), text[end:]


def changelog(prev, tag, date, summary=""):
    with open(CHANGELOG) as f:
        head, unreleased, rest = split(f.read())
    version = tag.removeprefix("v")
    if re.search(rf"^## \[{re.escape(version)}\]", rest, re.M):
        sys.exit(f"{CHANGELOG} already has {version}")

    found = generated(prev) if prev else {}
    body = [summary.strip(), ""] if summary.strip() else []
    if unreleased:
        body += [unreleased, ""]
    elif not prev:
        body += ["Initial release.", ""]
    else:
        print(f"Nothing was written under Unreleased; listing the commits that shipped since {prev}.", file=sys.stderr)
        for title in ("Added", "Changed", "Fixed"):
            if found[title]:
                body += [f"### {title}", "", *[f"- {i}" for i in found[title]], ""]
    if found.get("Dependencies"):
        body += ["### Dependencies", "", *[f"- {i}" for i in found["Dependencies"]], ""]

    entry = f"## [{version}] - {date}\n\n" + "\n".join(body).strip() + "\n\n"
    links = re.sub(r"^\[Unreleased\]: .*$", f"[Unreleased]: {compare(tag, 'HEAD')}", rest, count=1, flags=re.M)
    links = re.sub(r"^(\[Unreleased\]: .*\n)", rf"\g<1>[{version}]: {compare(prev, tag)}\n", links, count=1, flags=re.M)
    with open(CHANGELOG, "w") as f:
        f.write(head + "\n" + entry + links.lstrip("\n"))


def section(tag):
    """TAG's entry without its heading, and the comparison with the release before."""
    with open(CHANGELOG) as f:
        text = f.read()
    version = tag.removeprefix("v")
    m = re.search(rf"^## \[{re.escape(version)}\][^\n]*\n(.*?)(?=^## \[|^\[[^\]]+\]: |\Z)", text, re.M | re.S)
    if not m:
        sys.exit(f"{CHANGELOG} has no entry for {version}")
    link = re.search(rf"^\[{re.escape(version)}\]: (\S+)", text, re.M)
    out = m.group(1).strip() + "\n"
    if link and "/compare/" in link.group(1):
        out += f"\n**Full Changelog**: {link.group(1)}\n"
    return out


if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    if cmd == "changelog":
        changelog(args[0], args[1], args[2], args[3] if len(args) > 3 else "")
    elif cmd == "section":
        sys.stdout.write(section(args[0]))
    elif cmd == "shipped":
        sys.exit(0 if next(commits(args[0]), None) else 1)
    else:
        sys.exit(f"unknown command: {cmd}")
