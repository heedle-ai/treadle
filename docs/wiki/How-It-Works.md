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

`loom skills -catalog LINK` lists what a repository offers: every folder up to three levels down with a `SKILL.md`, with its name and description, or why it can't be imported as it is (`Catalog`).

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

For a change to a skill, `-review` prints `-approve NAME@VERSION -replacing CURRENT`. It shows control and format characters in the files as `\u` escapes, so a file can't drive your terminal, and it lists the files git takes for binary, whose contents the diff can't show: read those yourself. drawdown lists imports waiting for review and shows the same review, read only.

## A skill is a command, pinned by its plan

[[/images/skill.svg|alt=A skill step: compile with the store's newest skills, giving a plan node whose activity is Skill and whose arguments are the skill's name, the version it pins, then the program's arguments; then the check recompiles with the plan's own pins; then a pirn agent step runs with the skill's folder at that version in /work/skill beside the inputs in /work/in; then the run's commit records the outputs beside uses/<name>, the version's tree]]

`Library.AddTo` registers each skill's newest version as a draft command named after it (`Spec`). Its plan nodes run the activity `Skill`, with two [Bound](https://github.com/heedle-ai/draft/wiki/Adding-Commands) arguments first: the skill's name and the version. So a plan says exactly which version of each skill it uses. A skill named like a command the worker already has is left out and reported (`loom` and drawdown print it), so one bad skill never stops a worker.

A run must be checked against what it was compiled with, not what is newest when it runs, so loom's check uses `Library.Pinned`: the worker's commands, with each skill the plan uses registered at the version the plan pins. A pin must be a version that was **added** to that skill (the tree of a commit on its ref) and pass `Check` again; any other tree in the store is refused, even one whose `SKILL.md` has the skill's name, as an agent could write into its `/work/out`. A version that wasn't added, or one skill pinned to two versions, fails the check (`ProgramPlanMismatch`); a store that fails to answer is retried. And loom records each pinned version's tree under the run's `uses/<name>` (`Library.Uses`), so the record keeps the skill's exact folder wherever its history is cloned or pushed. These are loom's `Checker.RegistryFor` and `Recorder.Uses`; `loom worker` and drawdown set both.

## A skill step

`Command` is the `Skill` activity. It reads the pinned version's folder from the library, checks it was added as a version of the skill it's named as, and runs one of pirn's agent steps (`pirn.Step`) with the folder in `/work/skill`, its `SKILL.md` in Claude's first message, the step's inputs in `/work/in`, and the instruction `Use the skill "weather" with these arguments: "Boston".` Everything else is an agent step's: a fresh container with no network, every command recorded, the files in `/work/out` kept. A version that wasn't added to the skill fails at once with `SkillSetup`, before any container starts. A skill's files are written into the container without their file modes, so its scripts are run with `sh`, as the example does.

## Laws

A skill's steps are agent steps, so their answers can differ between runs; what holds is that a run is always checked against, run with and recorded with the versions its plan pins (`TestLaw_SkillRunsArePinned`), and loom's soundness laws 5 and 11 are stated with those pins.

## Where it stops

- **New skills need a restart.** A worker registers the store's skills when it starts.
- **Submitting needs the store too.** `loom compile` and `loom run` take `-store`, so a program can use the store's skills; drawdown uses its own `-store`.
- **Review is by repository, not by author.** A trusted prefix trusts every branch of every repository under it, and whoever can push there.
- **Reviews happen at the terminal.** drawdown shows them, but approving from the page is **Planned** (roadmap 3.8).
