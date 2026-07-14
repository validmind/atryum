---
name: release-notes
description: Draft the CHANGELOG.md section for an upcoming atryum release. Use before running `just release <tag>` — release-push publishes the matching CHANGELOG section as the curated top half of the GitHub release notes. Optionally takes the intended tag (e.g. v0.3.0) as an argument.
---

# Release notes

## Why this exists

`just release <tag>` prepends the CHANGELOG.md section matching the tag to
GitHub's auto-generated release notes (`--generate-notes` supplies the PR
list, contributor credits, and compare link; the changelog section supplies
the human-readable story). If no matching section exists, the release still
goes out with generated notes only — so the changelog is the part that takes
judgment, and it's this skill's job.

## What good notes look like

The audience is someone **operating or upgrading atryum**, not someone who
watched the commits land. They want to know what changed in behavior, what
they must do before or after upgrading, and whether anything is
security-relevant. Branch names, internal refactor jargon, and commit
shorthand don't communicate that — describe the effect, not the diff.

Follow [Keep a Changelog 1.0.0](https://keepachangelog.com/en/1.0.0/):
group entries under `Added` / `Changed` / `Deprecated` / `Removed` /
`Fixed` / `Security`, omitting empty groups. The `Security` section
deserves particular care in this project — auth gating, judge hardening,
trust boundaries, and anything an operator would want to prioritize an
upgrade for belongs there, even if the commit called it a "fix" or "feat".

## How to build the section

1. Find the previous release tag (`git tag --sort=-creatordate`, ignore
   backup/scratch tags) and review everything since:
   `git log <prev>..HEAD`. PR titles alone hide detail — a single PR here
   can carry a dozen meaningful commits, so read the commit subjects (and
   bodies where the subject is vague) before summarizing.
2. Decide or confirm the version. Semver from the reader's perspective:
   behavior-visible features → minor, only fixes → patch, breaking
   config/API/CLI changes → call them out explicitly and bump accordingly.
3. Update `CHANGELOG.md`:
   - New heading `## [x.y.z] - YYYY-MM-DD` (today's date, no `v` prefix —
     release-push strips the `v` from the tag when matching).
   - Fold in anything sitting under `[Unreleased]`, leaving that section
     present but empty.
   - Keep the compare links at the bottom of the file in sync.
4. Sanity-check the release path: `just release <tag>` builds from a
   pristine local clone of the tag (at `../atryum-release-<tag>`) and
   reads the notes from the tag's own `CHANGELOG.md` — so the order is
   commit the changelog, tag, push the tag, then release. Anything not
   committed and tagged simply isn't in the release, notes included.

Show the drafted section to the user before they tag — the changelog is a
judgment call about emphasis, and they may know context the log doesn't
show (a feature that's half-shipped, a fix that shouldn't be advertised).
