## A skill, and its versions

A skill is a folder: `SKILL.md`, whose YAML front matter has the skill's `name` and `description` ([Anthropic's Agent Skills](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/overview) format rules: `treadle.Meta`; the API's ban on "anthropic" and "claude" in names isn't applied, since treadle never uploads skills to Anthropic), then whatever scripts and references it needs. `treadle.Check` also applies pirn's rules for what may go in a step's container: at most 200 files and 16 MiB, with safe relative paths only.

`Library.Add` keeps a folder in a warp store as a commit on `refs/treadle/skills/<name>`, whose tree is the folder byte for byte. A **version** is that tree's ID: equal folders are equal versions, and adding a folder a skill already has changes nothing. Each new version's commit has the previous one as its parent, so `git log refs/treadle/skills/weather` is the skill's history and every version stays readable. Writers adding at the same time retry on a conflict, so they all agree. Besides Anthropic's rules, a name can't be one draft reserves (`block`, `script`, `pipe`, `par`, `when`, `guard`), `name` and `description` must be YAML strings, the front matter ends at a line that is exactly `---`, and `SKILL.md` is at most 1 MiB (`MaxSkillMD`), with no control characters other than tabs and line ends (a NUL would make git hide it from a review's diff) and no invisible format characters such as bidirectional overrides or zero-width spaces, apart from the zero-width joiner emoji use.

| Call | What it does |
|---|---|
| `Library.Add(files, at)` | Keeps a folder as its skill's newest version |
| `Library.Skills()` | The newest version of every skill; a ref whose newest version isn't a valid skill of its name is left out and reported, never fatal |
| `Library.Versions(name)` | Every version of one skill, newest first; a version that isn't a valid skill of that name is left out |
| `Library.Folder(name, version)` | One version's files, with sizes, and its `SKILL.md`, only for a version added to that skill, within the limits |
| `Library.Files(version)` | A version's folder, checked again |

## Importing skills

A skill can come from four places, and each goes through the same checks before it is kept:

| From | How | Call |
|---|---|---|
| A folder on disk | `loom skills -add FOLDER` | `ReadFolder`, then `Library.Add` |
| A pasted `SKILL.md` | `loom skills -add -` (reads standard input) | `Library.Add` with one file |
| A GitHub link to a folder | `loom skills -import https://github.com/OWNER/REPO/tree/REF/PATH` | `ParseGitHub`, then `Library.Import` |
| Any Git repository | `loom skills -import URL -path PATH [-branch REF]` (an `https://` or `file://` URL) | `Library.Import` |

`loom skills -catalog LINK` lists what a repository offers: every folder up to three levels down with a `SKILL.md`, with its name and description, or why it can't be imported as it is (`Catalog`). Each entry's source names the commit the catalog was read at, and importing that source (as drawdown's **Browse a repository** does for each skill you pick) takes exactly that commit, or nothing, saying the repository has changed since it was listed (`Fetch`, `TestInv_ImportPinsTheCommitSeen`).

Fetching is the `git` CLI's: a shallow, sparse clone of just the skill's folder (only files named `SKILL.md`, for a catalog) into a temporary directory, removed afterwards. The files are then read from Git's own objects (`ls-tree`, then `cat-file`), not from the checked-out folder, so they are the commit's exact bytes whatever the repository's `.gitattributes` or your settings say, and their number and sizes are checked before any is read. git never prompts for credentials (your own setup, such as `gh auth setup-git`, still applies to private repositories), runs no hooks from the repository, and is given the URL after `--`, so a URL can't pass as an option. It runs in its own process group, killed with its helpers if the fetch is cancelled, and a fetch with no deadline of its own stops after 5 minutes (`FetchTimeout`). A source must be an `https://` or `file://` URL without credentials, a plain branch or tag name, and a path of segments made of letters, digits, `.`, `_` and `-`; hidden files and folders in the skill's folder are left out, and a symbolic link or submodule refuses it. The download itself is git's: a huge file in the chosen folder is downloaded before the size check refuses it, within the timeout.

An imported version's commit names where it came from, in trailers: `Skill-Source` (the repository), `Skill-Source-Ref`, `Skill-Source-Path`, and `Skill-Source-Commit`, the exact commit read. `Skills` and `Versions` read them back (`Skill.Source`), so a skill's history says where every version came from:

```text
skill pdf

Skill-Source: https://github.com/anthropics/skills
Skill-Source-Ref: main
Skill-Source-Path: skills/pdf
Skill-Source-Commit: ca45fc496a3436251f191354091d557da31c15b5
```

[Anthropic's skills repository](https://github.com/anthropics/skills) imports as it is, all but two folders, which its catalog lists with the reason: `claude-api`, whose description is longer than the 1024 characters Agent Skills allow, and `template`, whose folder isn't named like its skill.

## Publishing a skill

[[/images/publish.svg|alt=String diagram of publish ; import: your skill's newest approved version is checked (no secret, no link, inside its folder), then written to skills/NAME with its README row and committed with a Treadle-Version trailer; you push it; a friend's heedle fetches it pinned to the commit and reviews it if it asks for connectors; publish ; import is the identity]]

`Library.Publish(ctx, name, dir)` (`loom skills publish NAME -repo DIR`) writes a skill's newest approved version into a clone of a repository of yours, at `skills/NAME/`: its files exactly, the bytes `Library.Files` gives, as regular files (treadle keeps no modes, so none are lost). Publishing again replaces that folder; a skill removed from your library stays in the repository until you remove it. It sets the skill's row in `skills/README.md`, between `<!-- treadle:skills -->` and `<!-- /treadle:skills -->` (name, description, the connectors it asks for, and the version it came from), and leaves every other row and line of the file as they are. Then it checks that what git would commit is the version exactly: the folder's tree, as git stages it, must be the version's tree ID (when your repository and the store use the same hash), so a `.gitignore` that leaves a file out, a line-ending setting, a filter such as Git LFS, or a disk that folds case can't change what's published unseen; otherwise it puts the folder and README back and says so. Then it commits those two paths, and nothing else you may have changed, with the `git` CLI and your repository's own identity, with a `Treadle-Version: VERSION` trailer; the same version again commits nothing. git runs without hooks, signing, prompts or a pager, and without `GIT_DIR`, `GIT_INDEX_FILE`, `GIT_CONFIG_*` and the like from the environment, so nothing outside can send the commit elsewhere (`TestInv_PublishTouchesNothingOfYours`). **It never pushes:** it prints the push for you to run, `git -C DIR push -u origin HEAD` (`TestInv_PublishNeverPushes`).

Before writing anything it refuses:

- a skill with no approved version: one waiting for review is never published (`ErrNotApproved`);
- files that look like they hold a secret (`ErrSecret`, naming the file and line): anything a key check matches, in a file or its path, such as `sk-ant-`, GitHub tokens (`ghp_`, `github_pat_`), AWS keys (quoted names too), private-key blocks, GitLab, Hugging Face, npm, Slack (tokens and webhooks), Stripe, Google and OpenAI keys (`SecretIn`);
- a path that would leave the skill's folder, and a symbolic link at `skills/`, `skills/NAME/`, `skills/README.md` or inside the folder, so nothing is written through one;
- a folder that isn't the top of a Git repository;
- changes of yours in `skills/NAME/` or `skills/README.md` (uncommitted, untracked or ignored files, such as a `.DS_Store` or `__pycache__/`, which it lists, and files marked assume-unchanged or skip-worktree), so a draft README is never committed unread and no file of yours is deleted; the README's new text is built from HEAD's; and a repository git can't commit in: no identity, a detached HEAD, a merge, rebase or cherry-pick going on. If it can't finish (cancelled, a full disk, git refusing), it puts the folder and README back as HEAD has them.

Each README cell is one line, with `|`, `$` (GitHub's math) and every character Markdown reads specially escaped and `<`, `>` and `&` written as entities, so a description can't end its cell, start a link or an image, or inject HTML on GitHub (`TestInv_PublishedReadmeCantBeBroken`). Publishing then importing is the identity: the imported version has the same files, byte for byte, the same modes and the same description, from the commit published (`TestLaw_PublishThenImportIsIdentity`, 40 random skill folders). An import from your repository is an import like any other: from a repository your friend doesn't trust, or asking for connectors, it waits for their review.

## Trust and review

A skill's scripts run in Claude's sandbox, but its `SKILL.md` steers what Claude does there, so an import from a repository you don't control waits for someone to read it first.

[[/images/review.svg|alt=Importing a skill: if its repository is under a trusted prefix, it becomes the skill's newest version at once; otherwise it waits on the skill's pending ref, where review shows its files and its diff from the newest version; then approving exactly that version makes it the newest version, and rejecting it closes the review]]

| Call | What it does |
|---|---|
| `Library.Import(source, at)` | From a trusted repository, keeps the skill as its newest version. From any other, keeps it on `refs/treadle/pending/<name>` and returns it with `Pending` set. Importing the newest version again changes nothing |
| `Library.Pending()` | The imports waiting for review |
| `Library.Review(name)` | The import waiting: its files, its whole `SKILL.md`, the version it would replace (`Replaces`), its diff from that version (from nothing, for a new skill), and the files the diff can't show because git takes them for binary (`Binary`) |
| `Library.Approve(name, version, replaces, at)` | Adds the import as the skill's newest version, only if `version` is exactly the one waiting and the skill's newest version is still `replaces`, the one the review's diff is from; its commit gets `Skill-Approved-From: <pending commit>` beside its source |
| `Library.Reject(name, version, at)` | Closes the review of exactly that version, with a `Skill-Rejected` commit on the pending ref |
| `Library.Trust`, `Untrust`, `Trusted` | See below |

The trusted prefixes are kept in the store, on `refs/treadle/config`. A trusted prefix is a repository URL with a path, such as `https://github.com/your-org`; it trusts that URL and anything under it at a `/`, so it never trusts `https://github.com/your-org-evil`. No prefix or repository URL may have an empty, `.` or `..` segment, so a URL names a place one way only and no prefix can trust a whole host or folder tree by accident. `Untrust` of a prefix that isn't listed exactly is an error, so a misspelling never leaves one trusted. A folder on disk and a pasted `SKILL.md` are yours, so `Add` keeps them without review. An import is open for review until it's approved (its tree becomes a version of the skill) or rejected; importing again after a rejection opens a new review. Approving binds what was reviewed: if another import arrived since, or the skill's newest version changed (so the diff you read is from the wrong place), `Approve` refuses and asks for a new review. A decision closes the review with a commit on the pending ref that moves it only if nobody else has, so an approval and a rejection made at once never both succeed; if the skill changes in the moment between an approval's check and its add, the review is reopened and the approval fails. An approval binds the bytes reviewed; the source it records is that of the import waiting, the one the latest review showed. Re-importing any version but the newest, even one the skill had before, also waits, since adding it would change what the skill runs.

```bash
loom skills -store ~/skills.git -review pdf                 # source, files, SKILL.md, the diff, then the commands
loom skills -store ~/skills.git -approve pdf@8b61e7d0b4ba45033cdaf7948d77d7208853e427
loom skills -store ~/skills.git -reject pdf@8b61e7d0b4ba45033cdaf7948d77d7208853e427   # or turn it down
loom skills -store ~/skills.git -trust https://github.com/your-org
```

For a change to a skill, `-review` prints `-approve NAME@VERSION -replacing CURRENT`. It shows control and format characters in the files as `\u` escapes, so a file can't drive your terminal, and it lists the files git takes for binary, whose contents the diff can't show: read those yourself. drawdown lists imports waiting for review, shows the same review, and approves or rejects from it, by running loom's skills workflow on a worker.

## A skill is a command, pinned by its plan

[[/images/skill.svg|alt=A skill step: compile with the store's newest skills, giving a plan node whose activity is Skill and whose arguments are the skill's name, the version it pins, then the program's arguments; then the check recompiles with the plan's own pins; then a pirn agent step runs with the skill's folder at that version in /work/skill beside the inputs in /work/in; then the run's commit records the outputs beside uses/<name>, the version's tree]]

`Library.AddTo` registers each skill's newest version as a draft command named after it (`Spec`). Its plan nodes run the activity `Skill`, with two [Bound](https://github.com/heedle-ai/draft/wiki/Adding-Commands) arguments first: the skill's name and the version. So a plan says exactly which version of each skill it uses. A skill named like a command the worker already has is left out and reported (`loom` and drawdown print it), so one bad skill never stops a worker.

A run must be checked against what it was compiled with, not what is newest when it runs, so loom's check uses `Library.Pinned`: the worker's commands, with each skill the plan uses registered at the version the plan pins. A pin must be a version that was **added** to that skill (the tree of a commit on its ref) and pass `Check` again; any other tree in the store is refused, even one whose `SKILL.md` has the skill's name, as an agent could write into its `/work/out`. A version that wasn't added, or one skill pinned to two versions, fails the check (`ProgramPlanMismatch`); a store that fails to answer is retried. And loom records each pinned version's tree under the run's `uses/<name>` (`Library.Uses`), so the record keeps the skill's exact folder wherever its history is cloned or pushed. These are loom's `Checker.RegistryFor` and `Recorder.Uses`; `loom worker` and drawdown set both. Because each run is checked against the library as it is when the run starts, a skill added or approved while a worker runs needs no restart: compile with the store again (`loom compile -store`, `loom run -store`, or drawdown, which reads the store's newest skills on every compile) and run.

## A skill step

`Command` is the `Skill` activity. It reads the pinned version's folder from the library, checks it was added as a version of the skill it's named as, and runs one of pirn's agent steps (`pirn.Step`) with the folder in `/work/skill`, its `SKILL.md` in Claude's first message, the step's inputs in `/work/in`, and the instruction `Use the skill "weather" with these arguments: "Boston".` Everything else is an agent step's: a fresh container with no network, every command recorded, the files in `/work/out` kept. A version that wasn't added to the skill fails at once with `SkillSetup`, before any container starts. `Library.StepGrant` makes the same checks (that, and connectors nobody reviewed) and gives the connectors the step would get, without running it: heedle's front door uses it to say whether a proposal can run. A skill's files are written into the container without their file modes, so its scripts are run with `sh`, as the example does.

## Connectors a skill uses

A skill that needs connectors ([pirn → Connectors](https://github.com/heedle-ai/pirn/wiki/How-It-Works#connectors)) says so in its front matter, as a space-separated string under `metadata`, where Anthropic's Agent Skills format keeps extra fields:

```yaml
---
name: triage
description: Triages this week's bugs by severity.
metadata:
  connectors: "linear/search_issues mail"
---
```

A step running the skill is granted exactly that (`treadle.Connectors`), narrowed by the worker's connectors: a worker without them fails the step as setup before any container starts. The program can't widen it. The grant is part of the skill's files, so a review shows it (drawdown flags it in red), and approving the skill approves it. So a skill that asks for connectors always waits for review, even from a trusted prefix, and a step gets its connectors only if a person chose that version: approved it, or added it by hand. A version that came in only by a trusted import fails as setup; import it again and approve it. A malformed one, or front matter with any key twice, refuses the skill, and `Skill.Connectors` lists it wherever skills are listed.

## Creating a skill from a description

`loom skills -store DIR -create "Sum the second column of a CSV file" -agent-image ID` (or **Describe a skill** in drawdown) has Claude write the skill. The description is checked first (1 to 2 KiB of plain text, no control or invisible characters), before any agent runs. Then an agent step (`treadle.Create`) is told the description and to write `SKILL.md`, any scripts, and a test, `tests/test.sh`, which it runs until it passes, all in `/work/out` of a fresh container like any agent step's (`CreateInstruction`). Those files are the skill, checked as any folder is; a folder that isn't a valid skill, or whose files weren't all kept, is refused. The skill then waits for review like an untrusted import (`Library.Propose`), its commit carrying the description (`Skill-Created-From`), which its version keeps once approved and `Skill.Created` lists. Read its files and its test in the review before approving: Claude wrote them.

## The first skills

treadle ships three skills, in `skills/`. `trip-planner` is the [flagship](https://github.com/heedle-ai/heedle/wiki/Getting-Started#plan-a-trip): a skill and two connectors, no code. Given a destination, the first and last day and any interests, Claude researches it with the `search` connector, chooses and orders each day's places with the `maps` connector (opening hours that day, travel times between stops), and writes `itinerary.md`, a section per day and its sources, which its `scripts/check.sh` checks (`TestInv_TripPlannerAsksOnlyForSearchAndMaps`). It needs exactly `search maps`, so it waits for review when imported. It only looks things up: it's told never to call a tool that books, buys, saves, shares or changes anything, whatever its inputs or a page say. The grant `maps` is every tool of that connector, so for a guarantee rather than an instruction, list the connector's saving tools under `writes` (and set `approve`) in the worker's connectors file: then a call to one waits for you, whatever Claude does. Its check rejects a missing or extra day, a time that isn't 00:00 to 23:59, and a date that isn't a real `YYYY-MM-DD` day. The other two are the hello world's two commands as skills. `find-evidence` has Claude report, for each named source, a line `NAME: delay|on-time: SUMMARY` from the step's inputs (and any connectors the step was granted), checked by its `scripts/check.sh`. `assess-claim` judges a claim from such reports: its `scripts/assess.sh` decides the verdict, by exactly the hello world's `assess` rule, and Claude explains it. Import them with `loom skills -store DIR -add skills/find-evidence` (and `skills/assess-claim`) from a checkout of this repo. Their names differ from the hello world's deterministic `source` and `assess`, which drawdown's example keeps, so both are usable side by side.

`TestLaw_AssessSkillAgreesWithAssess` runs `assess.sh` and the hello world's `assess` on every subset of the hello world's sources, and checks their output is identical, and that every source report passes `check.sh`: the skill version reaches the same verdict.

## Laws

A skill's steps are agent steps, so their answers can differ between runs; what holds is that a run is always checked against, run with and recorded with the versions its plan pins (`TestLaw_SkillRunsArePinned`), and loom's soundness laws 5 and 11 are stated with those pins. Publishing then importing a skill is the identity on its files, modes and description (`TestLaw_PublishThenImportIsIdentity`).

## Where it stops

- **Submitting needs the store too.** `loom compile` and `loom run` take `-store`, so a program can use the store's skills; drawdown uses its own `-store`.
- **Review is by repository, not by author.** A trusted prefix trusts every branch of every repository under it, and whoever can push there.
- **The secret check is a key check.** It finds keys that look like keys, in text as it is; a password in prose, a key in a format it doesn't know, or one inside an archive (`.zip`, `.docx`) or another encoding (UTF-16), isn't found. Read what you publish.
- **Trusting is done at the terminal.** drawdown can import, add, approve and reject (through loom's skills workflow on a worker), but trusted prefixes are set only with `loom skills -trust`.
