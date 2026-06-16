#!/usr/bin/env bash
set -euo pipefail

out_dir="${1:-license-reports/third-party}"
go_licenses="${GO_LICENSES:-github.com/google/go-licenses/v2@v2.0.1}"
license_checker="${LICENSE_CHECKER:-license-checker@25.0.1}"
allowed_go_licenses="${ALLOWED_GO_LICENSES:-Apache-2.0,BSD-3-Clause,CC-BY-4.0,ISC,MIT,0BSD,Unlicense}"
allowed_npm_licenses="${ALLOWED_NPM_LICENSES:-Apache-2.0;BSD-3-Clause;CC-BY-4.0;ISC;MIT;0BSD;Unlicense}"
go_notice_file="${GO_NOTICE_FILE:-}"

rm -rf "$out_dir"
mkdir -p "$out_dir/licenses/go" "$out_dir/licenses/npm"
out_dir="$(cd "$out_dir" && pwd)"
cp LICENSE "$out_dir/ATRYUM_LICENSE"
cp NOTICE "$out_dir/ATRYUM_NOTICE"

go run "$go_licenses" csv --ignore atryum ./... > "$out_dir/go-licenses.csv"
go run "$go_licenses" check --ignore atryum --allowed_licenses "$allowed_go_licenses" ./...
go run "$go_licenses" save --ignore atryum --save_path "$out_dir/licenses/go" --force ./...

(cd ui && npm install --ignore-scripts --no-audit --no-fund)
(cd ui && npx --yes "$license_checker" --production --json --excludePrivatePackages --onlyAllow "$allowed_npm_licenses" --start . --out "$out_dir/npm-production-licenses.json")
node scripts/copy_npm_license_files.js "$out_dir/npm-production-licenses.json" "$out_dir/licenses/npm"

{
  echo "Atryum Third-Party Notices"
  echo "=========================="
  echo
  echo "This bundle contains third-party dependency license metadata and license files"
  echo "for the distributed Atryum binary and embedded UI. It also includes Atryum's"
  echo "own LICENSE and NOTICE files for single-binary distributions."
  echo
  echo "Atryum license: ATRYUM_LICENSE"
  echo "Atryum notice: ATRYUM_NOTICE"
  echo "Go dependency inventory: go-licenses.csv"
  echo "Go license files: licenses/go/"
  echo "npm production dependency inventory: npm-production-licenses.json"
  echo "npm production license file index: npm-production-license-files.tsv"
  echo "npm production license files: licenses/npm/"
  echo
  echo "The project LICENSE and NOTICE files should be distributed alongside this file."
} > "$out_dir/THIRD_PARTY_NOTICES"

if [ -n "$go_notice_file" ]; then
  go run ./scripts/embed_notices.go "$out_dir" "$go_notice_file"
fi

echo "Third-party notices written to $out_dir"
