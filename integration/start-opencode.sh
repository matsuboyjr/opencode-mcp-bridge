#!/bin/sh
# Added for isolated test workspaces and data. No host project is mounted.
set -eu
mkdir -p /work/a /work/a/nested /work/a-other /work/b "$HOME"
for directory in /work/a /work/a/nested /work/a-other /work/b; do
  git -C "$directory" init -q
  git -C "$directory" config user.name Bridge-Test
  git -C "$directory" config user.email bridge-test@example.invalid
  printf 'isolated fixture\n' > "$directory/fixture.txt"
  git -C "$directory" add fixture.txt
  git -C "$directory" commit --allow-empty -qm fixture
done
export OPENCODE_CONFIG="/test/config-${TEST_API_VERSION}.json"
cd /work/a
opencode --version
exec opencode serve --hostname 0.0.0.0 --port 4096
