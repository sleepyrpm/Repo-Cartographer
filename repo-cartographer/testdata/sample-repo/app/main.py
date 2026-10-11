# fixture: part of repo-cartographer's testdata/sample-repo, not a real project
"""Entrypoint: build the pipeline, feed it a few events, print the result."""

from .config import load_settings
from .core import Pipeline


def main():
    settings = load_settings({"verbose": True})
    pipeline = Pipeline(settings)
    print(pipeline.run([{"id": i} for i in range(10)]))


if __name__ == "__main__":
    main()
