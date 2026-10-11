// fixture: part of repo-cartographer's testdata/sample-repo, not a real project
// Dashboard entrypoint.
const { fetchRows } = require("./api");
const { formatRow } = require("../shared/format.js");

async function render(target) {
  const rows = await fetchRows();
  target.textContent = rows.map(formatRow).join("\n");
}

module.exports = { render };
