#!/bin/sh
# Chart the clock's stored history with gnuplot and open the result.
#
#   ./plot.sh                  fetch the history from the clock, then plot it
#   ./plot.sh history.csv      plot an existing CSV (the clock is not contacted)
#   ./plot.sh [-n] [-o out.png] [history.csv]
#
# -n          don't open the image afterwards
# -o out.png  output file (default: miaoclock-history.png in the current directory)
#
# When fetching, MIAOCLOCK_DEVICE selects the clock (see README). The history
# command takes about a minute.
set -eu

open_it=1
out=miaoclock-history.png
while getopts no: opt; do
	case $opt in
	n) open_it=0 ;;
	o) out=$OPTARG ;;
	*) echo "usage: $0 [-n] [-o out.png] [history.csv]" >&2; exit 2 ;;
	esac
done
shift $((OPTIND - 1))

dir=$(cd "$(dirname "$0")" && pwd)
csv=${1:-}

command -v gnuplot >/dev/null 2>&1 || { echo "plot.sh: gnuplot not found (brew install gnuplot)" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if [ -z "$csv" ]; then
	if [ -x "$dir/miaoclock" ]; then
		set -- "$dir/miaoclock"
	elif command -v miaoclock >/dev/null 2>&1; then
		set -- miaoclock
	else
		set -- go run "$dir"
	fi
	echo "fetching history from the clock (about a minute)..." >&2
	"$@" history > "$tmp/history.csv"
	csv=$tmp/history.csv
fi

[ -s "$csv" ] || { echo "plot.sh: $csv is missing or empty" >&2; exit 1; }
[ "$(wc -l < "$csv")" -gt 1 ] || { echo "plot.sh: $csv has no records" >&2; exit 1; }

# gnuplot can't parse the timezone offset in the timestamps (2026-11-16T17:00:00-08:00).
sed -E 's/([0-9]{2}:[0-9]{2}:[0-9]{2})[-+][0-9]{2}:[0-9]{2}/\1/' "$csv" > "$tmp/h.csv"

gnuplot -c "$dir/plot.gp" "$tmp/h.csv" "$out"
echo "wrote $out" >&2

if [ "$open_it" = 1 ]; then
	if command -v open >/dev/null 2>&1; then
		open "$out"
	elif command -v xdg-open >/dev/null 2>&1; then
		xdg-open "$out"
	fi
fi
