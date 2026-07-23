#!/usr/bin/env python3
"""Offline documentation and workflow integrity checks for hook-service.

Standard-library only (no network, no third-party packages). Checks:

1. Links: every relative Markdown link and file reference resolves to a file
   that exists in the tree. External (http/https/mailto) and pure anchors are
   skipped.
2. Stale scope phrases: pre-implementation "foundation/future" scope language
   that has been corrected must not reappear in Markdown.
3. Workflow pinning: every third-party ``uses:`` in .github/workflows is pinned
   to a full 40-character commit SHA with a version comment. Calls to the
   organization's reusable workflows may use the documented pre-release ref
   ``@main`` (org policy) or a SHA.

Run: ``python3 scripts/validate-docs.py`` from the repository root. Exits
non-zero after printing every problem found.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

SKIP_DIRS = {".git", "node_modules", "dist", "bin"}

# Markdown link/image targets: [text](target) and ![alt](target).
MD_LINK = re.compile(r"!?\[[^\]]*\]\(([^)]+)\)")

# Clearly stale, pre-implementation scope phrases that must stay removed. These
# are specific enough not to collide with legitimate prose.
STALE_PHRASES = (
    "before delivery execution",
    "boundaries needed before delivery",
    "later phase",
    "next phase may",
    "remaining gates in",
    "do not add the public ingest",
    "when delivery functionality is introduced",
    "(future)",
)

# A `uses:` reference and its ref portion.
USES_LINE = re.compile(r"^\s*(?:-\s*)?uses:\s*(?P<value>\S+)")
# An organization reusable workflow call: owner/.github/.github/workflows/x.yml@ref
REUSABLE_WORKFLOW = re.compile(
    r"^[\w.-]+/\.github/\.github/workflows/[\w.-]+\.ya?ml@(?P<ref>\S+)$"
)
# A SHA-pinned action line, including the required version comment.
PINNED_ACTION_LINE = re.compile(
    r"uses:\s*[\w.-]+/[\w.-]+(?:/[\w.-]+)*@[0-9a-f]{40}\s+#\s*\S+"
)
# The documented pre-release refs allowed for reusable workflow calls.
ALLOWED_WORKFLOW_REFS = {"main"}


def markdown_files() -> list[Path]:
    files: list[Path] = []
    for path in REPO_ROOT.rglob("*.md"):
        if any(part in SKIP_DIRS for part in path.relative_to(REPO_ROOT).parts):
            continue
        files.append(path)
    return sorted(files)


def check_links() -> list[str]:
    failures: list[str] = []
    for path in markdown_files():
        rel = path.relative_to(REPO_ROOT)
        text = path.read_text(encoding="utf-8")
        for match in MD_LINK.finditer(text):
            target = match.group(1).strip()
            # Strip an optional "title" after the URL: (url "title").
            target = target.split(" ", 1)[0]
            if target.startswith(("http://", "https://", "mailto:", "#")):
                continue
            local = target.split("#", 1)[0]
            if not local:
                continue
            dest = (path.parent / local).resolve()
            if not dest.exists():
                failures.append(f"{rel}: broken link to '{target}'")
    return failures


def check_stale_phrases() -> list[str]:
    failures: list[str] = []
    for path in markdown_files():
        rel = path.relative_to(REPO_ROOT)
        text = path.read_text(encoding="utf-8").lower()
        for phrase in STALE_PHRASES:
            if phrase in text:
                failures.append(f"{rel}: stale scope phrase present: '{phrase}'")
    return failures


def check_workflow_pins() -> list[str]:
    failures: list[str] = []
    workflow_dir = REPO_ROOT / ".github" / "workflows"
    if not workflow_dir.is_dir():
        return failures
    for path in sorted(workflow_dir.glob("*.yml")):
        rel = path.relative_to(REPO_ROOT)
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            match = USES_LINE.match(line)
            if not match:
                continue
            value = match.group("value").strip("'\"")
            if value.startswith("./"):
                continue  # local composite/workflow reference
            reusable = REUSABLE_WORKFLOW.match(value)
            if reusable:
                ref = reusable.group("ref")
                if ref in ALLOWED_WORKFLOW_REFS or re.fullmatch(r"[0-9a-f]{40}", ref):
                    continue
                failures.append(
                    f"{rel}:{lineno}: reusable workflow ref '@{ref}' is not the "
                    f"documented pre-release ref (@main) or a commit SHA"
                )
                continue
            if not PINNED_ACTION_LINE.search(line):
                failures.append(
                    f"{rel}:{lineno}: '{line.strip()}' must be pinned to a "
                    "40-char commit SHA with a '# version' comment"
                )
    return failures


CHECKS = (
    ("links", check_links),
    ("stale phrases", check_stale_phrases),
    ("workflow pins", check_workflow_pins),
)


def main() -> int:
    total = 0
    for name, fn in CHECKS:
        failures = fn()
        total += len(failures)
        if failures:
            print(f"FAIL {name}")
            for failure in failures:
                print(f"  - {failure}")
        else:
            print(f"PASS {name}")
    if total:
        print(f"\n{total} problem(s) found")
        return 1
    print("\nall documentation checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
