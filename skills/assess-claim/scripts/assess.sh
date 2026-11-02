# assess.sh CLAIM REPORT... prints the verdict on CLAIM from evidence
# reports, each a file whose first line is NAME: SIGNAL[: SUMMARY], SIGNAL
# being delay (for the claim) or on-time (against it). It is the same rule,
# and the same output, as the hello world's assess command.
claim=$1
shift
for_=""
against=""
for f in "$@"; do
  line=$(head -n 1 "$f")
  name=${line%%: *}
  rest=${line#*: }
  signal=${rest%%: *}
  if [ "$name" = "$line" ]; then
    echo "assess: $f isn't a report: $line" >&2
    exit 2
  fi
  case $signal in
    delay) for_="$for_$name
" ;;
    on-time) against="$against$name
" ;;
    *) echo "assess: $f has unknown signal $signal" >&2; exit 2 ;;
  esac
done
list() {
  if [ -z "$1" ]; then echo "(none)"; else printf '%s' "$1" | LC_ALL=C sort | paste -sd, - | sed 's/,/, /g'; fi
}
verdict="NOT SUPPORTED"
[ -n "$for_" ] && verdict="SUPPORTED"
printf '%s: %s\n  for:     %s\n  against: %s\n' "$claim" "$verdict" "$(list "$for_")" "$(list "$against")"
