# fixture: part of repo-cartographer's testdata/sample-repo, not a real project
"""Test file so the fixture has a realistic test target."""

from app.core import Pipeline


def test_pipeline_counts_rows():
    result = Pipeline({"batch_size": 2}).run([{"id": 1}, {"id": 2}, {"id": 3}])
    assert result["rows"] == 2
