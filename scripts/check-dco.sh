#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: $0 <base> <head>" >&2
	exit 2
fi

base=$1
head=$2

git rev-parse --verify "${base}^{commit}" >/dev/null
git rev-parse --verify "${head}^{commit}" >/dev/null

status=0
checked=0

while IFS= read -r sha; do
	[ -n "$sha" ] || continue
	checked=$((checked + 1))

	author_name=$(git show -s --format=%an "$sha")
	author_email=$(git show -s --format=%ae "$sha")
	body=$(git show -s --format=%B "$sha")

	if ! grep -qE '^Signed-off-by: .+ <[^>]+>$' <<<"$body"; then
		echo "DCO: missing Signed-off-by in ${sha:0:12} (${author_name} <${author_email}>)" >&2
		status=1
		continue
	fi

	if grep -Fqx "Signed-off-by: ${author_name} <${author_email}>" <<<"$body"; then
		continue
	fi

	case "$author_email" in
		"49699333+dependabot[bot]@users.noreply.github.com")
			if grep -Fqx "Signed-off-by: dependabot[bot] <support@github.com>" <<<"$body"; then
				continue
			fi
			;;
		"41898282+github-actions[bot]@users.noreply.github.com")
			if grep -Fqx "Signed-off-by: github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>" <<<"$body"; then
				continue
			fi
			;;
	esac

	echo "DCO: sign-off does not match author in ${sha:0:12} (${author_name} <${author_email}>)" >&2
	status=1
done < <(git rev-list --no-merges "${base}..${head}")

if [ "$status" -eq 0 ]; then
	echo "DCO: ${checked} commit(s) checked."
fi

exit "$status"
