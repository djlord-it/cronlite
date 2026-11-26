#!/usr/bin/env python3
"""Deterministic package shards and fail-closed CI result/coverage aggregation."""
import argparse
import fnmatch
import json
import math
from pathlib import Path
import re
import subprocess
import sys


def packages():
    """Discover every root-module package, including packages without tests."""
    output = subprocess.check_output([
        "go", "list", "-f",
        "{{.ImportPath}} {{if or .TestGoFiles .XTestGoFiles}}test{{else}}none{{end}}", "./...",
    ], text=True)
    return {name: kind == "test" for name, kind in (line.split() for line in output.splitlines())}


def partition(names, weights, count):
    if count < 1 or len(names) < count or len(names) != len(set(names)):
        raise ValueError("shards require unique packages and 1 <= count <= package count")
    costs = {name: float(weights.get(name, 1)) for name in names}
    if any(not math.isfinite(value) or value <= 0 for value in costs.values()):
        raise ValueError("package durations must be finite positive seconds")
    shards, loads = [[] for _ in range(count)], [0.0] * count
    for name in sorted(names, key=lambda name: (-costs[name], name)):
        index = min(range(count), key=lambda index: (loads[index], index))
        shards[index].append(name)
        loads[index] += costs[name]
    return [sorted(shard) for shard in shards]


EMBED = re.compile(r"^\s*//go:embed\s+(.+)$")
EMBED_TOKEN = re.compile(r'\s*("(?:[^"\\]|\\.)*"|`[^`]*`|[^\s"`]+)')
MATCH_ALL = "*"


def embed_arguments(text):
    """Split go:embed arguments like Go: bare, "interpreted" or `raw` tokens.
    Returns None when the line cannot be parsed reliably."""
    tokens, position, text = [], 0, text.rstrip()
    while position < len(text):
        match = EMBED_TOKEN.match(text, position)
        if not match or match.end() == position:
            return None
        token = match.group(1)
        if token.startswith('"'):
            try:
                token = json.loads(token)
            except ValueError:
                return None
        elif token.startswith("`"):
            token = token[1:-1]
        tokens.append(token)
        position = match.end()
    return tokens or None


def broad_pattern(pattern):
    """Go and Python globs differ for classes and escapes ([^x] vs [!x]); any
    pattern using them matches everything so classification stays fail-closed.
    Python "*" also crosses "/", which is broader than Go's path.Match."""
    return MATCH_ALL if "[" in pattern or "\\" in pattern else pattern


def embed_patterns(root):
    """Repository-relative go:embed patterns; matched broadly (fnmatch "*" crosses "/")."""
    patterns = []
    for source in root.rglob("*.go"):
        relative = source.relative_to(root)
        if any(part.startswith(".") for part in relative.parts):
            continue
        for line in source.read_text(errors="replace").splitlines():
            match = EMBED.match(line)
            if match:
                arguments = embed_arguments(match.group(1))
                if arguments is None:
                    patterns.append(MATCH_ALL)
                    continue
                for pattern in arguments:
                    pattern = broad_pattern(pattern.removeprefix("all:"))
                    patterns.append(pattern if pattern == MATCH_ALL else
                                    str(relative.parent / pattern).removeprefix("./"))
    return patterns


def classify(event, paths, patterns):
    """Return True when the full pipeline must run. Only Markdown outside embeds may skip."""
    if event != "pull_request" or not paths:
        return True
    for path in paths:
        if not path.lower().endswith(".md") or path != path.strip() or path.startswith("/"):
            return True
        parts = path.split("/")
        ancestors = ["/".join(parts[:index]) for index in range(1, len(parts) + 1)]
        if any(fnmatch.fnmatchcase(candidate, pattern)
               for pattern in patterns for candidate in ancestors):
            return True
    return False


def check_gate(needs, required, skippable=()):
    for label, names in (("required", required), ("skippable", skippable)):
        if len(names) != len(set(names)):
            raise ValueError(f"duplicate {label} job IDs: {sorted(names)}")
    if set(needs) != set(required) or not required:
        raise ValueError(f"gate dependency mismatch: expected {sorted(required)}, got {sorted(needs)}")
    skippable = set(skippable)
    if not skippable <= set(required) or "changes" in skippable:
        raise ValueError("skippable checks must be required checks other than changes")
    full = "true"
    if skippable:
        changes = needs.get("changes", {})
        if changes.get("result") != "success":
            raise ValueError(f"change classification did not succeed: {changes.get('result')}")
        full = changes.get("outputs", {}).get("full")
        if full not in ("true", "false"):
            raise ValueError(f"change classification output is invalid: {full!r}")
    allowed = lambda name: {"success", "skipped"} if full == "false" and name in skippable else {"success"}
    failed = {name: value.get("result") for name, value in needs.items()
              if value.get("result") not in allowed(name)}
    if failed:
        raise ValueError(f"required checks did not succeed: {failed}")


TEST_ACTIONS = {"start", "run", "pause", "cont", "pass", "bench", "fail", "output", "skip"}


def check_results(events, selected):
    terminals = {}
    for event in events:
        name, action = event.get("Package"), event.get("Action")
        if "Package" not in event:
            # Build events carry ImportPath; output is informational, while a
            # build-fail (or anything else without a package) is rejected.
            if action == "build-output" and isinstance(event.get("ImportPath"), str) and event["ImportPath"]:
                continue
            raise ValueError(f"unexpected non-test event: {event}")
        if action not in TEST_ACTIONS:
            raise ValueError(f"invalid test event action in {name}: {action!r}")
        if name not in selected:
            raise ValueError(f"unexpected package in test output: {name}")
        if action == "fail":
            raise ValueError(f"test failure in {name}: {event.get('Test', '')}")
        if not event.get("Test") and action in ("pass", "skip"):
            if name in terminals:
                raise ValueError(f"duplicate package terminal: {name}")
            terminals[name] = action
    if set(terminals) != set(selected):
        raise ValueError("test output is incomplete: missing package terminal results")
    for name, has_tests in selected.items():
        if has_tests and terminals[name] != "pass":
            raise ValueError(f"package with tests was skipped: {name}")


PROFILE_ROW = re.compile(r"(.+\.go):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)")


def read_profile(path, selected):
    lines = path.read_text().splitlines()
    if not lines or lines[0] != "mode: atomic" or len(lines) < 2:
        raise ValueError(f"missing, empty or non-atomic coverage profile: {path}")
    rows, seen = [], set()
    for line in lines[1:]:
        match = PROFILE_ROW.fullmatch(line)
        if not match:
            raise ValueError(f"invalid coverage row in {path}: {line}")
        filename, start_line, start_col, end_line, end_col, statements, hits = match.groups()
        start, end = (int(start_line), int(start_col)), (int(end_line), int(end_col))
        location = f"{filename}:{start[0]}.{start[1]},{end[0]}.{end[1]}"  # canonical numbers
        if filename.rsplit("/", 1)[0] not in selected or start > end or start[0] < 1 or start[1] < 1:
            raise ValueError(f"invalid or foreign coverage location: {location}")
        if location in seen:
            raise ValueError(f"duplicate coverage block: {location}")
        seen.add(location)
        rows.append((location, int(statements), int(hits)))
    return rows


def read_names(path):
    names = path.read_text().splitlines()
    if not names or any(not name or name != name.strip() for name in names) or len(names) != len(set(names)):
        raise ValueError(f"invalid package manifest: {path}")
    return names


def merge_profiles(root, count, current):
    profiles = sorted(root.rglob("coverage.out"))
    if len(profiles) != count:
        raise ValueError(f"expected {count} coverage profiles, found {len(profiles)}")
    rows, selected_all, blocks = [], set(), set()
    for profile in profiles:
        directory = profile.parent
        selected = set(read_names(directory / "packages.txt"))
        if set(read_names(directory / "all-packages.txt")) != set(current):
            raise ValueError("package universe differs from current go list")
        if not selected <= set(current) or selected & selected_all:
            raise ValueError("shard package sets overlap or contain unknown packages")
        selected_all.update(selected)
        events = [json.loads(line) for line in (directory / "tests.json").read_text().splitlines()]
        check_results(events, {name: current[name] for name in selected})
        for row in read_profile(profile, selected):
            if row[0] in blocks:
                raise ValueError(f"overlapping shard coverage block: {row[0]}")
            blocks.add(row[0])
            rows.append(row)
    if selected_all != set(current):
        raise ValueError(f"shards omitted packages: {sorted(set(current) - selected_all)}")
    return sorted(rows)


def write_profile(path, rows):
    path.write_text("mode: atomic\n" + "".join(f"{location} {statements} {hits}\n"
                                             for location, statements, hits in rows))


def coverage(rows, label, threshold):
    total = sum(statements for _, statements, _ in rows)
    covered = sum(statements for _, statements, hits in rows if hits > 0)
    if total == 0:
        raise ValueError(f"{label}: no instrumented statements")
    percent = 100 * covered / total
    print(f"{label}: {percent:.1f}% (minimum {threshold}%)")
    if percent < threshold:
        raise ValueError(f"{label} coverage is below {threshold}%")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    shard = commands.add_parser("shard")
    shard.add_argument("--index", type=int, required=True, help="zero-based shard index")
    shard.add_argument("--count", type=int, required=True)
    shard.add_argument("--output", type=Path, required=True)
    shard.add_argument("--timings", type=Path, default=Path(__file__).with_name("ci_timings.json"))
    merge = commands.add_parser("merge")
    merge.add_argument("--input", type=Path, required=True)
    merge.add_argument("--count", type=int, required=True)
    merge.add_argument("--output", type=Path, default=Path("coverage.out"))
    gate = commands.add_parser("gate")
    gate.add_argument("--needs", required=True, help="JSON object from toJSON(needs)")
    gate.add_argument("--required", required=True, help="comma-separated required job IDs")
    gate.add_argument("--skippable", default="", help="jobs that docs-only pull requests may skip")
    changes = commands.add_parser("classify", help="read changed paths on stdin, print full=true|false")
    changes.add_argument("--event", required=True)
    args = parser.parse_args()
    if args.command == "gate":
        skippable = [name for name in args.skippable.split(",") if name]
        check_gate(json.loads(args.needs), args.required.split(","), skippable)
        print("All required checks succeeded or were legitimately skipped.")
    elif args.command == "classify":
        paths = sys.stdin.read().splitlines()
        full = classify(args.event, paths, embed_patterns(Path.cwd()))
        print(f"{len(paths)} changed paths; full pipeline: {str(full).lower()}", file=sys.stderr)
        print(f"full={str(full).lower()}")
    elif args.command == "shard":
        current = packages()
        weights = json.loads(args.timings.read_text())["seconds"]
        shards = partition(list(current), weights, args.count)
        if not 0 <= args.index < len(shards):
            raise ValueError("shard index is outside the configured range")
        args.output.mkdir(parents=True, exist_ok=True)
        manifest = "\n".join(shards[args.index]) + "\n"
        (args.output / "packages.txt").write_text(manifest)
        (args.output / "all-packages.txt").write_text("\n".join(sorted(current)) + "\n")
        print(manifest, end="")
    else:
        rows = merge_profiles(args.input, args.count, packages())
        filtered = [row for row in rows if ".gen.go:" not in row[0]]
        admin = [row for row in rows if "/internal/webadmin/" in row[0]]
        write_profile(args.output, rows)
        write_profile(args.output.with_name("coverage-filtered.out"), filtered)
        write_profile(args.output.with_name("coverage-admin.out"), admin)
        coverage(rows, "All packages", 40)
        coverage(filtered, "Excluding generated code", 40)
        coverage(admin, "Admin", 80)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"CI verification failed: {error}", file=sys.stderr)
        sys.exit(1)
