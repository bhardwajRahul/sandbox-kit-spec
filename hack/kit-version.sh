#!/usr/bin/env bash
# Version authority for the example kits.
#
# A kit's version lives in its descriptor: the agent kits carry it as the
# `version` arg's default — the installer pin their provide expands from —
# and the rest as the descriptor's own `version:`. For the kits whose
# version is somebody else's release, this script is the one place that
# knows where upstream publishes it, so the check report and the build
# cannot disagree about what "latest" means.
#
# gh is deliberately absent: its version authority is the nixpkgs pin in
# examples/gh/gh.dockerfile, so a bump is editing that pin (and the
# provides entry the pinned package dictates).
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Every kit whose version arg tracks an upstream release.
TRACKED=(claude claude-mixin codex codex-mixin gemini-mixin opencode opencode-mixin claude-acp codex-acp)

usage() {
	cat >&2 <<'EOF'
usage: hack/kit-version.sh <command> [kit]

  kits             the kits whose version tracks an upstream release
  upstream <kit>   the release upstream publishes (exit 1 when untracked)
  current <kit>    the version the descriptor records, or "dev"
  update <kit>     refresh the descriptor from upstream, print the version
  check            report every tracked kit against upstream
EOF
	exit 2
}

descriptor() { printf 'examples/%s/%s.yaml\n' "$1" "$1"; }

# npm answers with one line of JSON; sed keeps the dependency surface at curl.
npm_latest() {
	curl -fsSL "https://registry.npmjs.org/$1/latest" |
		sed -n 's/.*"version":"\([^"]*\)".*/\1/p'
}

upstream() {
	case "$1" in
	claude | claude-mixin)
		curl -fsSL https://downloads.claude.ai/claude-code-releases/latest
		;;
	codex | codex-mixin)
		# gh api is authenticated (no anonymous rate limit); curl is the
		# fallback for hosts without the gh CLI.
		local tag
		tag=$(gh api repos/openai/codex/releases/latest --jq .tag_name 2>/dev/null) ||
			tag=$(curl -fsSL https://api.github.com/repos/openai/codex/releases/latest |
				sed -n 's/.*"tag_name": "\([^"]*\)".*/\1/p')
		printf '%s\n' "${tag#rust-v}"
		;;
	gemini-mixin) npm_latest '@google%2Fgemini-cli' ;;
	opencode | opencode-mixin) npm_latest opencode-ai ;;
	claude-acp) npm_latest '@agentclientprotocol%2Fclaude-agent-acp' ;;
	codex-acp) npm_latest '@agentclientprotocol%2Fcodex-acp' ;;
	*) return 1 ;;
	esac
}

current() {
	local file
	file=$(descriptor "$1")
	[ -f "$file" ] || {
		printf 'dev\n'
		return
	}
	# The version arg's default outranks the descriptor's own version:
	# where both exist, the arg is what the build installs.
	awk '
		/^  version:$/           { inarg = 1 }
		inarg && /^    default:/ { argver = $2; gsub(/"/, "", argver); inarg = 0 }
		/^version:/              { topver = $2; gsub(/"/, "", topver) }
		END {
			if (argver != "") print argver
			else if (topver != "") print topver
			else print "dev"
		}
	' "$file"
}

# Refresh the descriptor from upstream and print the version to build as.
# An untracked kit, or an upstream that cannot be reached, leaves the
# descriptor alone and answers with what it already records — a build
# offline is still a build, just not a bump.
update() {
	local kit=$1 file latest tmp
	file=$(descriptor "$kit")
	if ! latest=$(upstream "$kit") || [ -z "$latest" ] || [ ! -f "$file" ]; then
		current "$kit"
		return
	fi
	if [ "$(current "$kit")" != "$latest" ]; then
		tmp=$(mktemp)
		awk -v version="$latest" '
			/^  version:$/           { inarg = 1 }
			inarg && /^    default:/ { sub(/"[^"]*"/, "\"" version "\""); inarg = 0 }
			{ print }
		' "$file" >"$tmp"
		mv "$tmp" "$file"
	fi
	printf '%s\n' "$latest"
}

check() {
	local kit have latest status
	for kit in "${TRACKED[@]}"; do
		have=$(current "$kit")
		latest=$(upstream "$kit" || true)
		if [ -z "$latest" ]; then
			status="? (upstream check failed)"
		elif [ "$have" = "$latest" ]; then
			status=ok
		else
			status="OUTDATED (latest $latest)"
		fi
		printf '%-16s %-10s %s\n' "$kit" "$have" "$status"
	done
}

[ $# -ge 1 ] || usage
command=$1
shift

case "$command" in
kits) printf '%s\n' "${TRACKED[@]}" ;;
check) check ;;
upstream | current | update)
	[ $# -eq 1 ] || usage
	"$command" "$1"
	;;
*) usage ;;
esac
