#!/usr/bin/env bash
# Writes packaging/THIRD-PARTY-LICENSES.txt: the licence of every piece of
# other people's code that is built into the program (Go packages compiled
# into it, and the JavaScript in the web app it serves).
#
# Run from the repository root after "cd web && npm ci" whenever go.mod or
# web/package.json changes, and commit the result:
#
#   bash packaging/third-party-licenses.sh
set -euo pipefail
out=packaging/THIRD-PARTY-LICENSES.txt
{
  echo "Local Media contains code written by other people. This file lists each"
  echo "piece, its version, where it comes from, and the full text of its licence."
  echo "(ffmpeg.exe and ffprobe.exe are separate programs; see THIRD-PARTY.txt.)"
  echo
  echo "Local Media itself is licensed under the GNU General Public License,"
  echo "version 3 or later; see LICENSE.txt."
  echo
  echo "================================================================================"
  echo "The Go programming language: its standard library and runtime ($(go env GOVERSION))"
  echo "https://go.dev"
  echo "================================================================================"
  echo
  cat "$(go env GOROOT)/LICENSE"
  # Go packages, for every system the program is built for.
  for os in windows linux darwin; do
    GOOS=$os GOARCH=amd64 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/localmedia
  done | sort -u | while read -r path version dir; do
    lic=$(ls "$dir" | grep -i -E '^(LICEN[SC]E|COPYING)(\.txt|\.md)?$' | head -1 || true)
    echo
    echo "================================================================================"
    echo "$path $version"
    echo "https://pkg.go.dev/$path"
    echo "================================================================================"
    echo
    if [ -n "$lic" ]; then cat "$dir/$lic"; else echo "(no licence file found in the package; see its project page)"; fi
  done
  # JavaScript built into the web app: the packages listed as dependencies
  # in web/package.json, and what those need at run time.
  for pkg in react react-dom scheduler hls.js; do
    dir=web/node_modules/$pkg
    version=$(node -p "require('./$dir/package.json').version")
    echo
    echo "================================================================================"
    echo "$pkg $version (JavaScript, part of the web app)"
    echo "https://www.npmjs.com/package/$pkg"
    echo "================================================================================"
    echo
    cat "$dir/LICENSE"
  done
} > "$out.new"
mv "$out.new" "$out"
echo "wrote $out: $(grep -c '^=====' "$out" | awk '{print $1/2}') pieces"
