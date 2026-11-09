#!/usr/bin/env bash
# Walks through the CI problem uncached explains, using a throwaway copy of
# examples/demo. Every "CI job" clones the repository afresh into the same
# directory, as a CI runner does, and all jobs share one GOCACHE, as a CI
# setup that caches the build cache does. Everything lives in a temporary
# directory that is removed at the end; the user's GOCACHE is not touched.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Go on Windows wants native paths; cygpath exists only under Git Bash/MSYS.
native() {
	if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

export GOCACHE GOFLAGS=-mod=mod GOWORK=off GOTOOLCHAIN=local GOPROXY=off CGO_ENABLED=0
GOCACHE=$(native "$work/gocache")
unset GODEBUG DEMO_REGION

export GIT_CONFIG_GLOBAL="$work/gitconfig" GIT_CONFIG_NOSYSTEM=1
: >"$GIT_CONFIG_GLOBAL"
export GIT_AUTHOR_NAME=demo GIT_AUTHOR_EMAIL=demo@example.com
export GIT_COMMITTER_NAME=demo GIT_COMMITTER_EMAIL=demo@example.com

bin="$work/bin/uncached$(go env GOEXE)"
(cd "$repo" && go build -o "$bin" ./cmd/uncached)

origin="$work/origin"
ci="$work/ci"
cp -R "$repo/examples/demo" "$origin"
commit() { # date message
	GIT_AUTHOR_DATE="$1" GIT_COMMITTER_DATE="$1" git -C "$origin" commit -q -am "$2"
}
git -C "$origin" init -q
git -C "$origin" add -A
commit "2026-11-02T10:00:00Z" "Add demo module"

step() { printf '\n### %s\n\n' "$*"; }
fresh_clone() {
	rm -rf "$ci"
	git clone -q "$origin" "$ci"
	# go test does not cache results that read files modified less than two
	# seconds earlier. A real CI job spends longer than that on setup.
	sleep 3
}
uncached() {
	printf '$ uncached %s\n' "$*"
	(cd "$ci" && "$bin" "$@") 2>&1
}

step "CI job 1: fresh clone, empty GOCACHE"
fresh_clone
uncached run ./...

step "CI job 2: fresh clone of the same commit, GOCACHE restored from job 1"
fresh_clone
uncached run ./...

step "CI job 3: the job now runs restore-mtime before the tests"
fresh_clone
uncached restore-mtime
uncached run ./...

step "CI job 4: fresh clone, restore-mtime, tests"
fresh_clone
uncached restore-mtime
uncached run ./...

step "CI job 5: a commit refactors money.go"
sed -i.bak 's/return fmt.Sprintf("%d.%02d %s", cents\/100, cents%100, symbol)/return fmt.Sprintf("%d.%02d", cents\/100, cents%100) + " " + symbol/' "$origin/money/money.go"
rm "$origin/money/money.go.bak"
commit "2026-11-03T09:30:00Z" "Simplify money.Format"
fresh_clone
uncached restore-mtime
uncached run ./...

step "CI job 6: the job sets DEMO_REGION"
fresh_clone
uncached restore-mtime
echo '$ export DEMO_REGION=us-east'
export DEMO_REGION=us-east
uncached run ./...

step "CI job 7: someone added -count=1 to the test command"
fresh_clone
uncached restore-mtime
uncached run -- -count=1 ./...
