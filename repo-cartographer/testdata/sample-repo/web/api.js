// fixture: part of repo-cartographer's testdata/sample-repo, not a real project
const { getJSON } = require("./client");
const { formatRow } = require("../shared/format");

async function fetchRows() {
  const rows = await getJSON("/api/rows");
  return rows.map(formatRow);
}

module.exports = { fetchRows };
