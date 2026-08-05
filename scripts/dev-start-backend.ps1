
# 仓库根由脚本自身位置推导 —— 别再写死绝对路径（上一个仓库搬家时这里全炸了）。
$RepoRoot = Split-Path -Parent $PSScriptRoot

$ErrorActionPreference = "Continue"
New-Item -ItemType Directory -Force -Path (Join-Path $RepoRoot "logs") | Out-Null
"backend launcher started $(Get-Date -Format o)" | Out-File -FilePath (Join-Path $RepoRoot "logs\backend.log") -Encoding utf8
$env:APP_ENV = "dev"
$env:ALLOWED_ORIGINS = "http://localhost:5173,http://127.0.0.1:5173"
$env:GOCACHE = (Join-Path $RepoRoot ".gocache")
Set-Location (Join-Path $RepoRoot "backend")
go run ./cmd 2>&1 | Tee-Object -FilePath (Join-Path $RepoRoot "logs\backend.log") -Append
