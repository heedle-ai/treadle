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

To run it, start a worker on that store with an Anthropic API key and an agent image, as for any agent step ([pirn → Agent steps](https://github.com/heedle-ai/pirn/wiki/Getting-Started)), `loom worker -store ~/skills-demo.git -agent-image <image ID>`, and submit with the same store: `loom run -store ~/skills-demo.git '(weather "Boston")'`. A worker registers the store's skills when it starts; restart it to pick up new ones. drawdown does the same with its `-store`.

## 4. Import skills

List what a repository offers, then import one by its GitHub link. Both run git, so you need the network:

```bash
cd ~/src/heedle-ai/loom
go run ./cmd/loom skills -catalog https://github.com/anthropics/skills
go run ./cmd/loom skills -store ~/skills-demo.git -import https://github.com/anthropics/skills/tree/main/skills/pdf
git --git-dir ~/skills-demo.git log -1 --format=%B refs/treadle/skills/pdf
```

The catalog lists each folder with its skill's name and description. The import prints the version (`added pdf at 8b61e7d0b4ba…` when this was written; it changes as the repository does), and the log shows where it came from, down to the commit. To keep a `SKILL.md` you have as text, paste it: `go run ./cmd/loom skills -store ~/skills-demo.git -add - < SKILL.md`.
