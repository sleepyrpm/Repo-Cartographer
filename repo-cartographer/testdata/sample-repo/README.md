<!-- fixture: part of repo-cartographer's testdata/sample-repo, not a real project -->
# Pulse

A tiny fake service used as a test fixture: a Python ingest pipeline, a
JavaScript dashboard and a Go report CLI.

- `app/main.py` - entrypoint, builds the pipeline and runs it.
- `app/core.py` - the pipeline hub everything else hangs off.
- `web/index.js` - dashboard entrypoint.
- `cmd/pulse/main.go` - CLI that prints a digest.
