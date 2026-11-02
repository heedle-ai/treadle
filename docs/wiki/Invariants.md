Every invariant below is guarded by a test or a CI step, and CI checks this page against the code on every push with `bash .github/invariants.sh`. Only the **Guarded by** column counts. Each test name listed there must exist and pass without being skipped; every guard test in the code must be listed.

[[/images/invariant-loop.svg|alt=From a mistake to a codified invariant: mistake, named invariant, guard, CI gate]]

| ID | Invariant | Guarded by | Came from |
|---|---|---|---|
| TREAD-1 | A skill's front matter follows Anthropic's Agent Skills rules (name of lower-case letters, digits and single hyphens, at most 64, without "anthropic" or "claude"; description of 1 to 1024 characters without `<` or `>`), and its folder pirn's (`pirn.CheckSkill`) | `TestInv_SkillsAreChecked` | Skills (roadmap 3.4) |
| TREAD-2 | Each version of a skill is a commit on `refs/treadle/skills/<name>` whose tree is the folder byte for byte; adding the same folder again changes nothing; every version stays readable | `TestInv_LibraryKeepsEveryVersion` | Skills (roadmap 3.4) |
| TREAD-3 | A skill can't take a command's name; a plan's pins must be versions the library has, of the skill they're named as, one version per skill | `TestInv_PinsAreChecked` | Skills (roadmap 3.4) |
| TREAD-4 | A run using a skill is checked against, runs with and is recorded with the version its plan pins, even after the skill changes; a fresh compile pins the newest | `TestLaw_SkillRunsArePinned` | Skills (roadmap 3.4): the same program compiles differently once a skill changes |
| TREAD-5 | A skill step whose version isn't in the library, or is another skill, fails with `SkillSetup`, non-retryable, before any container starts | `TestInv_UnknownVersionsFailAsSetup` | Skills (roadmap 3.4) |
| TREAD-6 | Getting Started's clone loop names exactly this repo and every sibling `go.mod` builds against | `TestInv_GettingStartedClonesEverySibling` | Carried over from pirn, where the list fell behind |
| TREAD-7 | The private repos bobbin, reed and pirn are checked out with their own read-only deploy keys; a missing key fails CI rather than skipping | `ci:private-bobbin`, `ci:private-reed`, `ci:private-pirn` | treadle builds against them |
