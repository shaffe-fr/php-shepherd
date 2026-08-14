# Demo Recording

This directory contains a [VHS](https://github.com/charmbracelet/vhs) tape file
for generating the terminal GIF used in the project README.

## Prerequisites

- [VHS](https://github.com/charmbracelet/vhs) installed (`go install github.com/charmbracelet/vhs@latest`)
- A working Shepherd installation with PHP 8.4 and 8.6 available via Herd

## Setup before recording

The demo expects:

```
~/projects/api/.phpversion        → contains "8.4"
~/projects/frontend/.phpversion   → contains "8.5"
```

And a git branch `bleeding-edge` in `~/projects/api` with `.phpversion` set to `8.6`.

Create the demo fixtures:

```powershell
mkdir -Force ~/projects/api, ~/projects/frontend
"8.4" | Set-Content ~/projects/api/.phpversion
"8.5" | Set-Content ~/projects/frontend/.phpversion

cd ~/projects/api
git init
git add .phpversion
git commit -m "php 8.4"
git checkout -b bleeding-edge
"8.6" | Set-Content .phpversion
git add .phpversion
git commit -m "php 8.6 for bleeding edge"
git checkout main
```

## Recording

```powershell
vhs demo/demo.tape
```

Output files:
- `demo/demo.gif` — for README embed
- `demo/demo.webm` — higher quality for blog posts / tweets
