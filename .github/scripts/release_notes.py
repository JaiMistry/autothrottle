#!/usr/bin/env python3
"""The changelog's entry for a release, and whether anything shipped.

    release_notes.py changelog PREV TAG DATE [SUMMARY [SECURITY]]
        moves CHANGELOG.md's Unreleased entries under TAG, released on DATE;
        SECURITY lists the IDs of the vulnerabilities it fixes
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

Dependencies are not a type of change: an update that matters to users
is written under its type like anything else. A release that only
updates dependencies says so in a sentence, naming what it ships. A
security release from the weekly scan lists what it fixes under Security,
each by its ID, linked to its advisory.
A section with nothing in it is left out.
"""
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


def updated(prev):
    """What a release that only updates dependencies says: what it ships,
    the Go version and modules, or that only the build's tools changed."""
    shipped = []
    for read in (images, go_requires):
        old, new = read(prev), read("HEAD")
        shipped += [f"{name} {version}" for name, version in sorted(new.items(), key=lambda i: (i[0] != "Go", i[0]))
                    if version != old.get(name) and "build" not in name]
    if not shipped:
        return "Updates to the tools AutoThrottle is built with only. Nothing else changes."
    listed = shipped[0] if len(shipped) == 1 else ", ".join(shipped[:-1]) + " and " + shipped[-1]
    return f"Dependency updates only: {listed}. Nothing else changes."


ADVISORIES = {"GO": "https://pkg.go.dev/vuln/{}", "CVE": "https://nvd.nist.gov/vuln/detail/{}",
              "GHSA": "https://github.com/advisories/{}"}


def advisories(ids, refs):
    """Security lines for the vulnerabilities a release fixes, each led by
    its ID as a reference to its advisory, added to refs."""
    lines = []
    for vuln in re.findall(r"(?:GO|CVE|GHSA)-[\w-]+", ids):
        refs[vuln] = ADVISORIES[vuln.split("-")[0]].format(vuln)
        lines.append(f"- [{vuln}] is fixed.")
    return lines


def person(name):
    """@name as a link: GitHub links mentions on a release page, but not in a file."""
    url = f"https://github.com/apps/{name.removesuffix('[bot]')}" if name.endswith("[bot]") else f"https://github.com/{name}"
    return f"[@{name}]({url})"


def linkify(text):
    """A bare #12 or @name written in an entry, as a link: #12 as a reference,
    defined at the bottom of the file. Ones already linked are left alone."""
    text = re.sub(r"(?<![\w\[/#])#(\d+)\b", r"[#\1]", text)
    return re.sub(r"(?<![\w\[/.@])@([A-Za-z0-9-]+(?:\[bot\])?)(?![\w\]])", lambda m: person(m.group(1)), text)


def compare(prev, tag):
    return f"https://github.com/{REPO}/compare/{prev}...{tag}" if prev else f"https://github.com/{REPO}/releases/tag/{tag}"


def generated(prev):
    """Added, Changed, Fixed from the commits, as {title: [lines]}. Updates
    to dependencies alone are left out."""
    sections = {"Added": [], "Changed": [], "Fixed": []}
    for sha, author, subject, files in commits(prev):
        word = subject.split()[0]
        if author.startswith("renovate") or (word in ("Update", "Bump") and all(f in DEP_FILES for f in files if ships(f))):
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


def changelog(prev, tag, date, summary="", security=""):
    with open(CHANGELOG) as f:
        head, unreleased, rest = split(f.read())
    version = tag.removeprefix("v")
    if re.search(rf"^## \[{re.escape(version)}\]", rest, re.M):
        sys.exit(f"{CHANGELOG} already has {version}")

    refs = {}
    found = generated(prev) if prev else {}
    body = [linkify(summary.strip()), ""] if summary.strip() else []
    if unreleased:
        body += [linkify(unreleased), ""]
    elif not prev:
        body += ["Initial release.", ""]
    else:
        print(f"Nothing was written under Unreleased; listing the commits that shipped since {prev}.", file=sys.stderr)
        for title in ("Added", "Changed", "Fixed"):
            if found[title]:
                body += [f"### {title}", "", *[f"- {i}" for i in found[title]], ""]
    fixes = advisories(security, refs)
    if fixes:
        # Security is the last type, so fixes join a Security section
        # written by hand when it ends the entry.
        headings = re.findall(r"^### (.+?)\s*$", unreleased, re.M)
        if headings and headings[-1] == "Security":
            body[-2] = body[-2].rstrip() + "\n" + "\n".join(fixes)
        else:
            body += ["### Security", "", *fixes, ""]
    if prev and not body:
        body = [updated(prev), ""]

    entry = f"## [{version}] - {date}\n\n" + "\n".join(body).strip() + "\n\n"
    links = re.sub(r"^\[Unreleased\]: .*$", f"[Unreleased]: {compare(tag, 'HEAD')}", rest, count=1, flags=re.M)
    links = re.sub(r"^(\[Unreleased\]: .*\n)", rf"\g<1>[{version}]: {compare(prev, tag)}\n", links, count=1, flags=re.M)
    # Each #12 in the entry links to its issue, unless already defined.
    for n in re.findall(r"\[#(\d+)\](?![(:])", entry):
        refs.setdefault(f"#{n}", f"https://github.com/{REPO}/issues/{n}")
    missing = [f"[{k}]: {v}\n" for k, v in refs.items() if not re.search(rf"^\[{re.escape(k)}\]: ", links, re.M)]
    if missing:
        links = links.rstrip("\n") + "\n" + "".join(missing)
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
    body = m.group(1).strip()
    out = body + "\n"
    if link and "/compare/" in link.group(1):
        out += f"\n**Full Changelog**: {link.group(1)}\n"
    # The references it uses, defined at the bottom of the file, come along.
    used = dict.fromkeys(re.findall(r"\[([^\[\]]+)\](?![(:\[])", body))
    defs = [d.group(0) for label in used if (d := re.search(rf"^\[{re.escape(label)}\]: \S+$", text, re.M))]
    if defs:
        out += "\n" + "\n".join(defs) + "\n"
    return out


if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    if cmd == "changelog":
        changelog(args[0], args[1], args[2], *args[3:5])
    elif cmd == "section":
        sys.stdout.write(section(args[0]))
    elif cmd == "shipped":
        sys.exit(0 if next(commits(args[0]), None) else 1)
    else:
        sys.exit(f"unknown command: {cmd}")
