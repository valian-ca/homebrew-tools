#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage: make-repo.sh <dir>

Turn the lanes demo into a throwaway git repository at <dir>, with one commit
on main and an origin remote: a bare repository at <dir>.origin.git, so that
origin/main exists. The main checkout also gets a git-ignored secrets.local,
which .worktreeinclude copies into new worktrees.

Examples:
  examples/lanes-demo/make-repo.sh ~/tmp/lanes-demo
  cd ~/tmp/lanes-demo && git worktree add ../lanes-demo-wt -b feat/try && cd ../lanes-demo-wt && lanes start
USAGE
}

case "${1:-}" in
  -h | --help) usage; exit 0 ;;
  "") usage >&2; exit 2 ;;
esac

src=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
dest=$1
if [ -e "$dest" ]; then
  echo "make-repo.sh: $dest already exists" >&2
  exit 1
fi

mkdir -p "$dest"
(cd "$src" && tar cf - --exclude make-repo.sh --exclude .lanes --exclude .demo --exclude app.env --exclude .env.local --exclude secrets.local .) | (cd "$dest" && tar xf -)
git -C "$dest" init -q -b main
git -C "$dest" add -A
git -C "$dest" -c user.name=lanes-demo -c user.email=lanes-demo@example.com commit -q -m "lanes demo"
git init -q --bare "$dest.origin.git"
git -C "$dest" remote add origin "$dest.origin.git"
git -C "$dest" push -q -u origin main
git -C "$dest" remote set-head origin main
echo "DEMO_SECRET=from-the-main-checkout" >"$dest/secrets.local"
echo "$dest"
