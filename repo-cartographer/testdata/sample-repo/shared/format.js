// fixture: part of repo-cartographer's testdata/sample-repo, not a real project
// Secondary hub: imported by both web/index.js and web/api.js.
function formatRow(row) {
  return `${row.id}: ${row.value ?? "-"}`;
}

module.exports = { formatRow };
