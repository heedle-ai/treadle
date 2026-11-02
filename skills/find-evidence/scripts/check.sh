# check.sh REPORT... exits non-zero, naming the file, unless each report's
# first line is NAME: SIGNAL[: SUMMARY], NAME lower-case letters, digits
# and hyphens, SIGNAL delay or on-time: what assess-claim reads.
status=0
for f in "$@"; do
  if ! head -n 1 "$f" | grep -Eq '^[a-z][a-z0-9-]*: (delay|on-time)(: .*)?$'; then
    echo "check: $f: want NAME: delay|on-time: SUMMARY, got: $(head -n 1 "$f")" >&2
    status=1
  fi
done
exit $status
