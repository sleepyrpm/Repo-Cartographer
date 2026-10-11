# fixture: part of repo-cartographer's testdata/sample-repo, not a real project
"""Leaf module: in-memory store, configured by the config hub."""

from .config import load_settings


class Store:
    def __init__(self, overrides=None):
        self.settings = load_settings(overrides)
        self.rows = []

    def put(self, row):
        self.rows.append(row)
        return len(self.rows)
