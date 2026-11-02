**treadle** keeps skills and turns each into a command. A skill is a folder whose `SKILL.md` says how to do a kind of work, with any scripts or references it needs, in the format of [Anthropic's Agent Skills](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/overview). Add one, and programs can use it by name:

```lisp
(pipe (browse "https://forecast.example/boston")
      (weather "Boston"))
```

Each `(weather …)` step is one of [pirn](https://github.com/heedle-ai/pirn/wiki)'s agent steps: Claude works in a fresh container with the skill's folder in `/work/skill` and the step's inputs in `/work/in`, and every command it runs is recorded.

## Why

A program should say *what* to do, and skills say *how*, so new kinds of work don't need new code. And a run must stay checkable after its skills change: when a program compiles, its plan pins the exact version of every skill it uses, the run is checked against those versions, and its record keeps them. Run the same program after editing a skill and `git diff` shows both what the skill became and what changed in the answers.

treadle reimplements nothing: Git keeps every version (through [warp](https://github.com/heedle-ai/warp/wiki)), pirn runs the step, and Temporal retries it.

## Where treadle fits

| Repo | Role |
|---|---|
| [weft](https://github.com/heedle-ai/weft/wiki) | Composition algebra |
| [draft](https://github.com/heedle-ai/draft/wiki) | Language: s-expressions that compile to shuttle plans |
| [shuttle](https://github.com/heedle-ai/shuttle/wiki) | Durable execution on Temporal |
| [bobbin](https://github.com/heedle-ai/bobbin/wiki) | A fresh Podman container per agent step |
| [reed](https://github.com/heedle-ai/reed/wiki) | A fresh browser per browsing step |
| [pirn](https://github.com/heedle-ai/pirn/wiki) | Claude for agent steps |
| **treadle** | Skills, as commands pinned by version (this repo) |
| [warp](https://github.com/heedle-ai/warp/wiki) | Content-addressed store |
| [loom](https://github.com/heedle-ai/loom/wiki) | Runs programs and records every run |
| [drawdown](https://github.com/heedle-ai/drawdown/wiki) | Web UI |
