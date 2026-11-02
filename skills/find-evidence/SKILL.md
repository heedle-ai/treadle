---
name: find-evidence
description: 'Finds evidence about a question for each named source (such as "weather" or "carrier") in the step''s inputs, and reports each as one line, "NAME: delay|on-time: SUMMARY", ready for assess-claim.'
---
# Find evidence

The step's arguments are the question, then the names of the sources to report on, such as `"will the shipment be late?" weather carrier factory`. The inputs in `/work/in` are what earlier steps found: pages, notes, answers.

For each source name:

1. Look through the inputs for what that source says about the question. If this step was granted connectors, you may search them too; say which you used.
2. Decide its signal: `delay` if the source points to the claim being true (something will be late), `on-time` if it points the other way. If nothing bears on it, use `on-time` and say "no evidence found".
3. Write one line to `/work/out/NAME.txt`: `NAME: SIGNAL: SUMMARY`, the summary one sentence, citing the input file it came from, like `(in/n0.txt)`.

Then check every report:

```sh
sh /work/skill/scripts/check.sh /work/out/*.txt
```

Fix any line it rejects. Reply with the reports, one per line. A report's first line is all a later assess-claim step reads.
