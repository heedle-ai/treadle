---
name: assess-claim
description: 'Judges a claim, such as "the shipment will be late", from evidence reports in the step''s inputs (lines like "weather: delay: Storm closes the port"), and says which reports support it and which don''t.'
---
# Assess a claim

The step's arguments are the claim. Each input in `/work/in` is an evidence report: one line `NAME: SIGNAL: SUMMARY`, where SIGNAL is `delay` (it supports the claim) or `on-time` (it doesn't).

1. Run the verdict script, giving it the claim and every input:

   ```sh
   sh /work/skill/scripts/assess.sh "the claim" /work/in/*.txt > /work/out/verdict.txt
   ```

   It prints the claim, `SUPPORTED` if any report signals `delay` (else `NOT SUPPORTED`), then which reports are for it and which against, sorted. If it exits non-zero, an input isn't a report: say which, and stop.
2. Read the summaries in the inputs and write two or three sentences in `/work/out/why.txt` explaining the verdict, citing reports by name.
3. Reply with the verdict script's output, then your explanation.

Don't change the verdict: the script decides it, so the same evidence always gives the same answer.
