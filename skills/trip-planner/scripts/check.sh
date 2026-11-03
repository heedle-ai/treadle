# check.sh ITINERARY FIRST LAST exits non-zero, saying why, unless the
# itinerary has a "## Day N, DATE: AREA" section for every day from FIRST
# to LAST (YYYY-MM-DD), in order, each with at least one stop
# "- HH:MM PLACE, ADDRESS: WHY", and a "## Sources" section with at least
# one https:// address.
f=$1 first=$2 last=$3
[ -r "$f" ] || { echo "check: can't read $f" >&2; exit 1; }
case "$first$last" in
  *[!0-9-]*|"") echo "check: dates are YYYY-MM-DD" >&2; exit 1;;
esac
d0=$(date -u -d "$first" +%s 2>/dev/null || date -u -j -f %Y-%m-%d "$first" +%s 2>/dev/null)
d1=$(date -u -d "$last" +%s 2>/dev/null || date -u -j -f %Y-%m-%d "$last" +%s 2>/dev/null)
[ -n "$d0" ] && [ -n "$d1" ] && [ "$d1" -ge "$d0" ] || { echo "check: $first to $last isn't a trip" >&2; exit 1; }
days=$(( (d1 - d0) / 86400 + 1 ))
[ "$days" -le 31 ] || { echo "check: at most 31 days" >&2; exit 1; }
status=0
n=1
while [ "$n" -le "$days" ]; do
  day=$(date -u -d "@$((d0 + (n - 1) * 86400))" +%Y-%m-%d 2>/dev/null || date -u -r "$((d0 + (n - 1) * 86400))" +%Y-%m-%d)
  head="## Day $n, $day: "
  if ! grep -q "^$head" "$f"; then
    echo "check: no section \"$head…\"" >&2
    status=1
  else
    # The section's stops: from its heading to the next heading.
    stops=$(awk -v h="$head" 'index($0, h) == 1 {on = 1; next} /^## / {on = 0} on' "$f" | grep -Ec '^- [0-2][0-9]:[0-5][0-9] [^,]+, [^:]+: .+')
    [ "$stops" -ge 1 ] || { echo "check: day $n has no stop \"- HH:MM PLACE, ADDRESS: WHY\"" >&2; status=1; }
  fi
  n=$((n + 1))
done
# Days in order: each "## Day N" heading's number is one more than the last.
awk '/^## Day [0-9]+, / {split($3, a, ","); if (a[1] != ++i) {print "check: day sections out of order at \"" $0 "\""; bad = 1}} END {exit bad}' "$f" >&2 || status=1
awk '/^## Sources/ {on = 1; next} /^## / {on = 0} on && /^- https:\/\/[^ ]+$/ {found = 1} END {exit !found}' "$f" || { echo "check: no \"## Sources\" with an https:// address" >&2; status=1; }
exit $status
