# Setup script for demo recording fixtures
# Run this before `vhs demo/demo.tape`

$ErrorActionPreference = "Stop"

$apiDir = Join-Path $HOME "projects/api"
$frontendDir = Join-Path $HOME "projects/frontend"

# Create project directories
New-Item -ItemType Directory -Force -Path $apiDir | Out-Null
New-Item -ItemType Directory -Force -Path $frontendDir | Out-Null

# Frontend project: PHP 8.5
Set-Content -Path (Join-Path $frontendDir ".phpversion") -Value "8.5" -NoNewline

# API project: PHP 8.4 on main, PHP 8.6 on bleeding-edge
Push-Location $apiDir

if (-not (Test-Path ".git")) {
    git init
}

# Main branch with PHP 8.4
$defaultBranch = (git symbolic-ref --short HEAD 2>$null)
if (-not $defaultBranch) { $defaultBranch = "main" }

Set-Content -Path ".phpversion" -Value "8.4" -NoNewline
git add .phpversion
git commit --allow-empty -m "project at php 8.4" 2>$null

# Bleeding-edge branch with PHP 8.6
git branch -D bleeding-edge 2>$null
git checkout -b bleeding-edge
Set-Content -Path ".phpversion" -Value "8.6" -NoNewline
git add .phpversion
git commit -m "upgrade to php 8.6" 2>$null

# Return to main
git checkout $defaultBranch

Pop-Location

Write-Host "Demo fixtures ready." -ForegroundColor Green
Write-Host "  $apiDir/.phpversion = 8.4 (main), 8.6 (bleeding-edge)"
Write-Host "  $frontendDir/.phpversion = 8.5"
