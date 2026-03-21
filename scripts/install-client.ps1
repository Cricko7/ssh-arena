#!/usr/bin/env pwsh
param(
  [string]$InstallDir = "$HOME\.ssh-arena\bin"
)

$ErrorActionPreference = 'Stop'

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Go toolchain is required to build the ssh-arena client. Install Go 1.24+ first."
}

$repoZip = 'https://github.com/Cricko7/ssh-arena/archive/refs/heads/main.zip'
$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("ssh-arena-client-" + [guid]::NewGuid())
$zipPath = Join-Path $tempRoot 'repo.zip'
$srcRoot = Join-Path $tempRoot 'src'
$repoRoot = Join-Path $srcRoot 'ssh-arena-main'
$binaryPath = Join-Path $InstallDir 'game-client.exe'

New-Item -ItemType Directory -Force -Path $tempRoot, $srcRoot, $InstallDir | Out-Null
Invoke-WebRequest -Uri $repoZip -OutFile $zipPath
Expand-Archive -Path $zipPath -DestinationPath $srcRoot -Force
Push-Location $repoRoot
$env:GOCACHE = Join-Path $tempRoot 'gocache'
go build -o $binaryPath ./cmd/game-client
Pop-Location

Write-Host "Installed ssh-arena client to $binaryPath"
Write-Host "Run it with: & '$binaryPath'"
