## A skill, and its versions

A skill is a folder: `SKILL.md`, whose YAML front matter has the skill's `name` and `description` ([Anthropic's Agent Skills](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/overview) rules: `treadle.Meta`), then whatever scripts and references it needs. `treadle.Check` also applies pirn's rules for what may go in a step's container: at most 200 files and 16 MiB, with safe relative paths only.

`Library.Add` keeps a folder in a warp store as a commit on `refs/treadle/skills/<name>`, whose tree is the folder byte for byte. A **version** is that tree's ID: equal folders are equal versions, and adding a folder a skill already has changes nothing. Each new version's commit has the previous one as its parent, so `git log refs/treadle/skills/weather` is the skill's history and every version stays readable. Writers adding at the same time retry on a conflict, so they all agree. Besides Anthropic's rules, a name can't be one draft reserves (`block`, `script`, `pipe`, `par`, `when`, `guard`), `name` and `description` must be YAML strings, the front matter ends at a line that is exactly `---`, and a description can't hold control characters.

| Call | What it does |
|---|---|
| `Library.Add(files, at)` | Keeps a folder as its skill's newest version |
| `Library.Skills()` | The newest version of every skill; a ref whose newest version isn't a valid skill of its name is left out and reported, never fatal |
| `Library.Versions(name)` | Every version of one skill, newest first |
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

Fetching is the `git` CLI's: a shallow, sparse clone of just the skill's folder (only `SKILL.md` files, for a catalog) into a temporary directory, removed afterwards. git never prompts for credentials (your own setup, such as `gh auth setup-git`, still applies to private repositories), runs no hooks from the repository, and is given the URL after `--`, so a URL can't pass as an option. A source must be an `https://` or `file://` URL, a plain branch or tag name, and a path of safe segments; hidden files and folders in the skill's folder are left out, and a symbolic link refuses it.

An imported version's commit names where it came from, in trailers: `Skill-Source` (the repository), `Skill-Source-Ref`, `Skill-Source-Path`, and `Skill-Source-Commit`, the exact commit read. `Skills` and `Versions` read them back (`Skill.Source`), so a skill's history says where every version came from:

```text
skill pdf

Skill-Source: https://github.com/anthropics/skills
Skill-Source-Ref: main
Skill-Source-Path: skills/pdf
Skill-Source-Commit: ca45fc496a3436251f191354091d557da31c15b5
```

[Anthropic's skills repository](https://github.com/anthropics/skills) imports as it is, all but two folders, which its catalog lists with the reason: `claude-api`, whose description is longer than the 1024 characters Agent Skills allow, and `template`, whose folder isn't named like its skill.

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
- **Every skill runs sandboxed, but nothing reviews it yet.** Trust levels for imported skills are **Planned** (roadmap 3.6).
