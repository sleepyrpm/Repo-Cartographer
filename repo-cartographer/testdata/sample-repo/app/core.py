# fixture: part of repo-cartographer's testdata/sample-repo, not a real project
"""The pipeline. Imported by main.py and tests/test_core.py."""

from .config import load_settings
from .metrics import timer
from .store import Store


class Pipeline:
    def __init__(self, overrides=None):
        self.settings = load_settings(overrides)
        self.store = Store(overrides)

    def run(self, events):
        elapsed = timer()
        for event in events[: self.settings["batch_size"]]:
            self.store.put(event)
        return {"rows": len(self.store.rows), "seconds": elapsed()}
