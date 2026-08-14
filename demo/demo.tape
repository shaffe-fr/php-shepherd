# Shepherd Demo — Per-project PHP version switching
# Record with: vhs demo/demo.tape
# Requires: VHS (https://github.com/charmbracelet/vhs)

Output demo/demo.gif
Output demo/demo.webm

Set Shell "pwsh"
Set FontSize 16
Set Width 900
Set Height 500
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 50ms
Set WindowBar Colorful
Set WindowBarSize 40

# --- Scene 1: Show two projects with different PHP versions ---

Type "# Two Laravel projects, two PHP versions"
Enter
Sleep 1s

Type "cat ~/projects/api/.phpversion"
Enter
Sleep 1.5s

Type "cat ~/projects/frontend/.phpversion"
Enter
Sleep 1.5s

# --- Scene 2: Automatic switching on cd ---

Type "cd ~/projects/api"
Enter
Sleep 500ms

Type "php -v"
Enter
Sleep 2s

Type "cd ~/projects/frontend"
Enter
Sleep 500ms

Type "php -v"
Enter
Sleep 2s

# --- Scene 3: The real use case — git checkout switches PHP version ---

Type ""
Enter
Sleep 500ms

Type "# Real scenario: switching to a bleeding-edge branch"
Enter
Sleep 1s

Type "cd ~/projects/api"
Enter
Sleep 500ms

Type "cat .phpversion"
Enter
Sleep 1.5s

Type "git checkout bleeding-edge"
Enter
Sleep 1.5s

Type "cat .phpversion"
Enter
Sleep 1.5s

Type "php -v"
Enter
Sleep 2s

Type "# PHP 8.6 — zero config, zero manual switch"
Enter
Sleep 2s

# --- Scene 4: Quick setup ---

Type ""
Enter
Sleep 500ms

Type "# Setup takes one command:"
Enter
Sleep 500ms

Type "shp use 8.6"
Enter
Sleep 2s

Type "# Done. Shepherd handles the rest."
Enter
Sleep 3s
