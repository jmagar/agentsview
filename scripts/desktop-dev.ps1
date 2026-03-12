<#
.SYNOPSIS
    Build and launch the AgentsView Tauri desktop app in dev mode on Windows.

.DESCRIPTION
    Builds the frontend SPA, compiles the Go sidecar binary, copies it into
    the Tauri binaries directory, and runs `cargo tauri dev`.

.PARAMETER SkipBuild
    Skip the frontend and Go sidecar build steps. Use this when iterating
    on Rust/Tauri code only and the sidecar binary is already up to date.
#>
param(
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"

# Ensure fnm-managed Node.js is on PATH. fnm requires an `fnm env`
# eval in each new PowerShell session; without it, node/npm are not
# found even though fnm itself is on PATH via WinGet links.
if ((Get-Command fnm -ErrorAction SilentlyContinue) -and -not (Get-Command node -ErrorAction SilentlyContinue)) {
    Write-Host "Activating fnm environment..." -ForegroundColor Yellow
    fnm env --use-on-cd --shell powershell | Out-String | Invoke-Expression
}

$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if (-not (Test-Path "$RepoRoot\go.mod")) {
    # Script may live at repo/scripts/ directly
    $RepoRoot = Split-Path -Parent $PSScriptRoot
}
if (-not (Test-Path "$RepoRoot\go.mod")) {
    Write-Error "Could not locate repository root (no go.mod found)"
    exit 1
}

$DesktopDir = Join-Path $RepoRoot "desktop"
$FrontendDir = Join-Path $RepoRoot "frontend"
$EmbedDir = Join-Path $RepoRoot "internal\web\dist"
$BinDir = Join-Path $DesktopDir "src-tauri\binaries"
$Triple = "x86_64-pc-windows-msvc"
$SidecarBin = Join-Path $BinDir "agentsview-$Triple.exe"

if (-not $SkipBuild) {
    # --- Build frontend ---
    Write-Host "Building frontend..." -ForegroundColor Cyan
    Push-Location $FrontendDir
    try {
        npm install
        npm run build
    } finally {
        Pop-Location
    }

    # Copy frontend dist into Go embed directory
    if (Test-Path $EmbedDir) {
        Remove-Item -Recurse -Force $EmbedDir
    }
    Copy-Item -Recurse (Join-Path $FrontendDir "dist") $EmbedDir

    # --- Build Go sidecar ---
    Write-Host "Building Go sidecar..." -ForegroundColor Cyan

    $version = git -C $RepoRoot describe --tags --always --dirty 2>$null
    if (-not $version) { $version = "dev" }
    $commit = git -C $RepoRoot rev-parse --short HEAD 2>$null
    if (-not $commit) { $commit = "unknown" }
    $buildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $ldflags = "-X main.version=$version -X main.commit=$commit -X main.buildDate=$buildDate"

    $env:CGO_ENABLED = "1"
    Push-Location $RepoRoot
    try {
        go build -tags fts5 -ldflags $ldflags -o agentsview.exe ./cmd/agentsview
    } finally {
        Pop-Location
    }

    # Copy sidecar binary
    if (-not (Test-Path $BinDir)) {
        New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    }
    Copy-Item (Join-Path $RepoRoot "agentsview.exe") $SidecarBin -Force

    Write-Host "Sidecar ready: $SidecarBin" -ForegroundColor Green
} else {
    if (-not (Test-Path $SidecarBin)) {
        Write-Error "Sidecar binary not found at $SidecarBin. Run without -SkipBuild first."
        exit 1
    }
    Write-Host "Skipping build (using existing sidecar binary)" -ForegroundColor Yellow
}

# --- Launch Tauri dev ---
# Use `cargo run` directly instead of `npx tauri dev` to avoid Tauri's
# built-in dev server opening a browser tab (port 1430) that shows a
# stale "Preparing your workspace" page. The app manages its own
# webview navigation from the splash screen to the Go backend.
Write-Host "Launching Tauri dev..." -ForegroundColor Cyan
Push-Location (Join-Path $DesktopDir "src-tauri")
try {
    cargo run
} finally {
    Pop-Location
}
