#!/usr/bin/env python3
"""Assemble the documentation site sources from the repository's own Markdown.

The site has no copy of the docs: each page is read from its place in the
repository (README.md, integration/*.md, ...) and written to site/_src with
its relative links rewritten, so the files keep working on GitHub too.

  * a link to a file that is a site page becomes a link to that page;
  * any other relative link (LICENSE, docs/specs/..., deploy/...) becomes an
    absolute link to the file or directory on GitHub.

Usage: python3 site/build.py   (then: mkdocs build -f site/mkdocs.yml --strict)
"""
import os
import posixpath
import re
import shutil
import sys

REPO_URL = "https://github.com/redfoxius/claude-memory"
BRANCH = "master"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "site", "_src")

# repository path -> page name in the site
PAGES = {
    "README.md": "index.md",
    "integration/INSTALL.md": "install.md",
    "integration/USAGE.md": "usage.md",
    "integration/ollama.md": "ollama.md",
    "integration/mcp-registration.md": "mcp-registration.md",
    "DEPLOY.md": "deploy.md",
    "SECURITY.md": "security.md",
    "CONTRIBUTING.md": "contributing.md",
}

LINK = re.compile(r"(\]\()([^)\s]+)(\))")


def rewrite(src_path: str, text: str) -> str:
    src_dir = posixpath.dirname(src_path)

    def repl(m: "re.Match[str]") -> str:
        target = m.group(2)
        if re.match(r"^([a-z][a-z0-9+.-]*:|#)", target, re.I):
            return m.group(0)  # absolute URL, mailto:, in-page anchor
        path, _, anchor = target.partition("#")
        resolved = posixpath.normpath(posixpath.join(src_dir, path))
        suffix = "#" + anchor if anchor else ""
        if resolved in PAGES:
            return m.group(1) + PAGES[resolved] + suffix + m.group(3)
        kind = "tree" if os.path.isdir(os.path.join(ROOT, resolved)) else "blob"
        return f"{m.group(1)}{REPO_URL}/{kind}/{BRANCH}/{resolved}{suffix}{m.group(3)}"

    return LINK.sub(repl, text)


def main() -> int:
    shutil.rmtree(OUT, ignore_errors=True)
    os.makedirs(OUT)
    for src, dst in PAGES.items():
        with open(os.path.join(ROOT, src), encoding="utf-8") as f:
            text = f.read()
        with open(os.path.join(OUT, dst), "w", encoding="utf-8") as f:
            f.write(rewrite(src, text))
    print(f"assembled {len(PAGES)} pages into {os.path.relpath(OUT, ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
