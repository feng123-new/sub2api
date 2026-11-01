# Deployed source provenance

The deployed application is `0.2.19`, built from personal commit
`eab0b311f2684465a7caba7b0177b7adb4752c40`. Its only parent is
`4663ccec0e96aaa0fe36f55f598d14482b58150f`. The maintenance work that adds
this document changes operations scripts and documentation; it does not change
the deployed binary or claim that the application runs the newer ops commit.

## Upstream integration

Official `v0.2.19` resolves to
`dece26153621959ccd7635814f4b2d08304e681a`. It is not an ancestor of the
deployed commit. Their merge base is official `v0.2.18`,
`ac1b8f1271be0d674fa6f074403c1410f30a3a81`.

Record the deployed release as a **content integration snapshot**, not an
upstream merge. Of the 259 paths changed between those official versions,
201 match the official target exactly and 58 changed while retaining personal
differences. No path in this set remains identical to the old upstream content.
This comparison does not prove semantic equivalence of every custom resolution.
Do not infer missing features from Git's ahead/behind commit counts.

The machine-readable record is [source-provenance-20261030.json](source-provenance-20261030.json).
Do not create an artificial merge or rewrite existing history to hide this
boundary. A future upstream merge must inspect and resolve actual differences;
do not disable the runbook's target-ancestry check for the resulting release.

## VPS source and build inputs

The canonical deployed Git checkout is
`/opt/sub2api-deploy/worktrees/deployed-eab0b311f268`. It has a detached HEAD
at the deployed commit and a clean tracked working tree. The separate ops
maintenance worktree is used for new operations commits.

The retained published archive is
`/opt/sub2api-deploy/ops/sub2api-v0219-index-maintenance-20261030/source.tar.gz`.
Its SHA-256 is
`ce5cf4e109da1ce253cfdbe6c1b18a7ef6255d174b0113e4b25ebb3506f75b8d`.
All 4,592 tracked files match after accounting for CRLF/LF differences in 150
text files; the original archive remains the authority for exact build-input
bytes, including embedded text. Its 179 generated frontend files were copied
to the deployed checkout and verified byte-for-byte without rebuilding.

Keep `runtime/manifest.json` bound to the running binary's commit and archive.
An ops-only GitHub main update is not an application deployment. Maintain the
installed ops revision separately in `ops/REVISION` and include it in backups.

Detailed VPS evidence is retained under
`/opt/sub2api-deploy/ops/source-backup-maintenance-20261102/`.
No application dependencies, builds, or tests run on WSL.
