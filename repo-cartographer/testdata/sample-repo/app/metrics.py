# fixture: part of repo-cartographer's testdata/sample-repo, not a real project
"""Leaf module: timing helpers, imports nothing from the repo."""

import time


def timer():
    start = time.time()
    return lambda: time.time() - start
