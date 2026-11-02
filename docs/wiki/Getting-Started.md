## 1. Install and clone

You need Go 1.24 or newer and Git. treadle, bobbin, reed and pirn are private, so clone with your GitHub login:

```bash
gh auth login && gh auth setup-git
mkdir -p ~/src/heedle-ai && cd ~/src/heedle-ai
for r in weft draft shuttle warp bobbin reed pirn treadle loom; do git clone https://github.com/heedle-ai/$r.git; done
cd treadle && go test ./...
```

## 2. Write a skill

A skill is a folder with a `SKILL.md`. Its front matter names and describes it: `name` is lower-case letters, digits and single hyphens (at most 64 characters), and `description` is 1 to 1024 characters, without `<` or `>`. The rest is what Claude should do.

```bash
cd ~/src/heedle-ai
mkdir -p weather/scripts
cat > weather/SKILL.md <<'END'
---
name: weather
description: Writes a one-line weather report for a city to out/weather.txt.
---
Run `sh /work/skill/scripts/report.sh CITY > /work/out/weather.txt`, with the
city you were given, then reply with what it wrote.
END
printf 'echo "$1: sunny, 21°C"\n' > weather/scripts/report.sh
```

## 3. Add it, and use it

Skills live in the same store a loom worker records runs in. `loom skills` adds a folder, or lists what's there; hidden files and folders in it (`.git`, `.DS_Store`) are left out, and symbolic links are refused:

```bash
cd ~/src/heedle-ai/loom
go run ./cmd/loom skills -store ~/skills-demo.git -add ../weather
go run ./cmd/loom skills -store ~/skills-demo.git
go run ./cmd/loom compile -store ~/skills-demo.git '(weather "Boston")'
```

```text
added weather at 254277ed5f35e5cfac9ab7e18f87d05061850245
weather              254277ed5f35  Writes a one-line weather report for a city to out/weather.txt.
```

The compiled plan's node runs the activity `Skill`, with the skill's name and the version it pins first, then the program's arguments:

```json
"activity": "Skill",
"args": ["weather", "254277ed5f35e5cfac9ab7e18f87d05061850245", "Boston"]
```

To run it, start a worker on that store with an Anthropic API key and an agent image, as for any agent step ([pirn → Agent steps](https://github.com/heedle-ai/pirn/wiki/Getting-Started)), `loom worker -store ~/skills-demo.git -agent-image <image ID>`, and submit with the same store: `loom run -store ~/skills-demo.git '(weather "Boston")'`. A skill added while the worker runs needs no restart: each run is checked against the store as it is then. drawdown, with its `-store`, compiles with the store's newest skills every time.

## 4. Import skills

List what a repository offers, then import one by its GitHub link. Both run git, so you need the network:

```bash
cd ~/src/heedle-ai/loom
go run ./cmd/loom skills -catalog https://github.com/anthropics/skills
go run ./cmd/loom skills -store ~/skills-demo.git -import https://github.com/anthropics/skills/tree/main/skills/pdf
```

The catalog lists each folder with its skill's name and description. The import isn't a skill yet: its repository isn't trusted, so it waits for review (`pdf at 8b61e7d0b4ba… waits for review` when this was written; the version changes as the repository does). Read it, then approve exactly the version you read, with the command `-review` prints at its end (this was the version when it was written; use the one yours prints):

```bash
go run ./cmd/loom skills -store ~/skills-demo.git -review pdf
go run ./cmd/loom skills -store ~/skills-demo.git -approve pdf@8b61e7d0b4ba45033cdaf7948d77d7208853e427
git --git-dir ~/skills-demo.git log -1 --format=%B refs/treadle/skills/pdf
```

The log shows where it came from, down to the commit, and the pending commit it was approved from. To skip review for repositories you trust, `-trust https://github.com/anthropics`. To keep a `SKILL.md` you have as text, paste it: `go run ./cmd/loom skills -store ~/skills-demo.git -add - < SKILL.md`.
