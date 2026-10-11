# fixture: part of repo-cartographer's testdata/sample-repo, not a real project
"""Settings loader. This module is the hub: core, store and main all import it."""

DEFAULTS = {"batch_size": 50, "store_path": "pulse.db", "verbose": False}


def load_settings(overrides=None):
    settings = dict(DEFAULTS)
    settings.update(overrides or {})
    return settings
