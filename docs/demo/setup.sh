#!/usr/bin/env bash
# Builds what the demo tapes record: ./bin/sf, the stand-in API from api.go
# running on a local port, and a profile pointing at it — all under
# /tmp/sf-demo, so nothing touches a real account or your own config.
#
# The tapes source it, so the environment it exports is the one they type in.
# The build runs in a subshell: set -e in the recording shell would close it
# the first time a tape shows a command failing on purpose.

demo=/tmp/sf-demo
addr=127.0.0.1:8484
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

(
	set -euo pipefail
	if [ -f "$demo/api.pid" ]; then
		kill "$(cat "$demo/api.pid")" 2>/dev/null || true
	fi
	rm -rf "$demo"
	mkdir -p "$demo/work"

	cd "$root"
	go build -o bin/sf ./cmd/sf
	go build -o "$demo/api" docs/demo/api.go

	"$demo/api" -addr "$addr" >"$demo/api.log" 2>&1 &
	echo $! >"$demo/api.pid"
	for _ in $(seq 50); do
		curl -fs "http://$addr/tags/index.json" >/dev/null && break
		sleep 0.1
	done
) || return 1 2>/dev/null || exit 1

export PATH="$root/bin:$PATH"
export SF_CONFIG_DIR="$demo/config" SF_NO_KEYRING=1
unset SF_PROFILE SF_API_URL SF_EMAIL SF_APIKEY SF_COMPANY_ID SF_MODULE NO_COLOR
sf auth login studio --api-url "http://$addr" --email demo@example.com \
	--api-key demo --company 1001 >/dev/null 2>&1
cd "$demo/work" || return 1
