## A skill, and its versions

A skill is a folder: `SKILL.md`, whose YAML front matter has the skill's `name` and `description` ([Anthropic's Agent Skills](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/overview) rules: `treadle.Meta`), then whatever scripts and references it needs. `treadle.Check` also applies pirn's rules for what may go in a step's container: at most 200 files and 4 MiB, with safe relative paths only.

`Library.Add` keeps a folder in a warp store as a commit on `refs/treadle/skills/<name>`, whose tree is the folder byte for byte. A **version** is that tree's ID: equal folders are equal versions, and adding a folder a skill already has changes nothing. Each new version's commit has the previous one as its parent, so `git log refs/treadle/skills/weather` is the skill's history and every version stays readable.

| Call | What it does |
|---|---|
| `Library.Add(files, at)` | Keeps a folder as its skill's newest version |
| `Library.Skills()` | The newest version of every skill |
| `Library.Versions(name)` | Every version of one skill, newest first |
| `Library.Files(version)` | A version's folder, checked again |

## A skill is a command, pinned by its plan

[[/images/skill.svg|alt=A skill step: compile with the store's newest skills, giving a plan node whose activity is Skill and whose arguments are the skill's name, the version it pins, then the program's arguments; then the check recompiles with the plan's own pins; then a pirn agent step runs with the skill's folder at that version in /work/skill beside the inputs in /work/in; then the run's commit records the outputs beside uses/<name>, the version's tree]]

`Library.AddTo` registers each skill's newest version as a draft command named after it (`Spec`). Its plan nodes run the activity `Skill`, with two [Bound](https://github.com/heedle-ai/draft/wiki/Adding-Commands) arguments first: the skill's name and the version. So a plan says exactly which version of each skill it uses. A skill can't take the name of a command the worker already has.

A run must be checked against what it was compiled with, not what is newest when it runs, so loom's check uses `Library.Pinned`: the worker's commands, with each skill the plan uses registered at the version the plan pins. A pin the library doesn't have, a version that is another skill, or one skill pinned to two versions fails the check (`ProgramPlanMismatch`). And loom records each pinned version's tree under the run's `uses/<name>` (`Library.Uses`), so the record keeps the skill's exact folder wherever its history is cloned or pushed. These are loom's `Checker.RegistryFor` and `Recorder.Uses`; `loom worker` and drawdown set both.

## A skill step

`Command` is the `Skill` activity. It reads the pinned version's folder from the library, checks it is the skill it's named as, and runs one of pirn's agent steps (`pirn.Step`) with the folder in `/work/skill`, its `SKILL.md` in Claude's first message, the step's inputs in `/work/in`, and the instruction `Use the skill "weather" with these arguments: "Boston".` Everything else is an agent step's: a fresh container with no network, every command recorded, the files in `/work/out` kept. A version that isn't in the library, or is another skill, fails at once with `SkillSetup`, before any container starts.

## Laws

A skill's steps are agent steps, so their answers can differ between runs; what holds is that a run is always checked against, run with and recorded with the versions its plan pins (`TestLaw_SkillRunsArePinned`), and loom's soundness laws 5 and 11 are stated with those pins.

## Where it stops

- **New skills need a restart.** A worker registers the store's skills when it starts.
- **Every skill runs sandboxed, but nothing reviews it yet.** Trust levels for imported skills are **Planned** (roadmap 3.6).
