#!/usr/bin/env python3
"""Sync Tango desktop and plugin toolchain versions from VERSION."""

import argparse
import json
import re
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SEMVER = r"\d+\.\d+\.\d+"
JSON_TARGETS = {
    "tan.json": [("version",)],
    "packages/plugin-ui/package.json": [("version",)],
    "packages/plugin-sdk/node/package.json": [("version",)],
    "tools/mincode-cli/toolchain.json": [
        ("mincode",),
        ("go", "github.com/mincodeai/plugin-sdk-go"),
        ("npm", "@mincodeai/plugin-ui"),
        ("npm", "@mincodeai/plugin-sdk"),
    ],
}
APP_VERSION = Path("backend/misc/misc.go")


def replace_version(text, field, prefix, version, path):
    pattern = rf'({field}")({prefix}{SEMVER})(")'
    updated, count = re.subn(pattern, lambda match: match[1] + version + match[3], text, flags=re.M)
    if count != 1:
        raise ValueError(f"{path}: expected one version for {field}")
    return updated


def changes(root, version):
    updates = {}
    for relative, fields in JSON_TARGETS.items():
        path = root / relative
        text = path.read_text()
        data = json.loads(text)
        updated = text
        for keys in fields:
            current = data
            for key in keys:
                current = current[key]
            prefix = "v" if keys[0] == "go" else ""
            expected = prefix + version
            if current != expected:
                field = rf'{re.escape(json.dumps(keys[-1]))}\s*:\s*'
                updated = replace_version(updated, field, prefix, expected, relative)
        if text != updated:
            updates[path] = updated

    path = root / APP_VERSION
    text = path.read_text()
    updated = replace_version(text, r"^var AppVersion = ", "", version, APP_VERSION)
    if text != updated:
        updates[path] = updated
    return updates


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="report stale files without modifying them")
    args = parser.parse_args()
    try:
        version = (REPO / "VERSION").read_text().strip()
        if not re.fullmatch(SEMVER, version) or any(int(part) > 65535 for part in version.split(".")):
            raise ValueError("VERSION must be MAJOR.MINOR.PATCH with each part <= 65535")
        updates = changes(REPO, version)
        if args.check and updates:
            parser.exit(1, "".join(f"out of date: {path.relative_to(REPO)}\n" for path in updates))
        for path, content in updates.items():
            path.write_text(content)
        print(version)
    except (OSError, TypeError, ValueError, KeyError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
