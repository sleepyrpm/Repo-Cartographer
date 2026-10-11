// fixture: part of repo-cartographer's testdata/sample-repo, not a real project
// Leaf module: the HTTP shim, imports nothing from the repo.
async function getJSON(path) {
  const response = await fetch(path);
  return response.json();
}

module.exports = { getJSON };
