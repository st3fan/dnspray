# Project

This is a reconnaissance tool for finding DNS servers within a supplied list of network prefixes.

The tool is written in Go.

# Method of working

1. Plan the work in a GitHub issue. Describe the proposed changes, scope, and validation, and have the user review the issue before implementation.
2. Implement the reviewed plan in a pull request for simple work.
3. For complicated work, use a stack of pull requests. Split the work into focused, reviewable layers, with foundational changes below the changes that depend on them.
4. Link the pull request or stack to the planning issue and explain how the changes were validated.
5. When the user asks to merge a pull request, treat the work as complete. After merging, switch back to the repository's default branch and update it from the remote so the next task starts there. Preserve any unrelated local changes.

Use the repository's [gh-stack skill](.agents/skills/gh-stack/SKILL.md) to manage stacked pull requests. Read and follow the skill before creating or modifying a stack, and create the stack before implementing its layers.

# Performance benchmarks

Run `python3 scripts/benchmark.py` from the homelab before requesting review of every pull request and commit its appended results in `BENCHMARK.md`, including the tested revision, environment, command, elapsed time, and discovered DNS servers so results can be compared across changes.

The user authorizes running `find-nameservers 192.168.0.0/24 192.168.53.0/24` for this benchmark. Development takes place in the `192.168.0.0/16` homelab; the benchmark scans the two specified `/24` prefixes.

Commit source changes before measuring, then commit the benchmark results separately. Keep historical entries. Include the median runtime and any discovery differences in the PR description. If homelab access is unavailable, document that limitation in `BENCHMARK.md` and the PR; do not report failed connectivity as a performance baseline.
