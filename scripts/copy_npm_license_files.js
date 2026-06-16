#!/usr/bin/env node
const fs = require("fs");
const path = require("path");

const [jsonPath, destRoot] = process.argv.slice(2);

if (!jsonPath || !destRoot) {
  console.error("Usage: copy_npm_license_files.js <license-checker.json> <destination>");
  process.exit(2);
}

const packages = JSON.parse(fs.readFileSync(jsonPath, "utf8"));
const rows = [];

fs.mkdirSync(destRoot, { recursive: true });

function safeName(name) {
  return name.replace(/[^a-zA-Z0-9._-]+/g, "_").replace(/^_+|_+$/g, "");
}

for (const [name, meta] of Object.entries(packages).sort(([a], [b]) => a.localeCompare(b))) {
  const source = meta.licenseFile;
  let copiedTo = "";

  if (source && fs.existsSync(source)) {
    const ext = path.extname(source) || ".txt";
    const target = `${safeName(name)}${ext}`;
    fs.copyFileSync(source, path.join(destRoot, target));
    copiedTo = `licenses/npm/${target}`;
  }

  rows.push({ name, licenses: meta.licenses || "UNKNOWN", copiedTo });
}

fs.writeFileSync(
  path.join(path.dirname(jsonPath), "npm-production-license-files.tsv"),
  ["package\tlicense\tlicense_file", ...rows.map((row) => `${row.name}\t${row.licenses}\t${row.copiedTo}`)].join("\n") + "\n",
);
