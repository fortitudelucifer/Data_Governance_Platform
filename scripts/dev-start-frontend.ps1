
# 仓库根由脚本自身位置推导 —— 别再写死绝对路径（上一个仓库搬家时这里全炸了）。
$RepoRoot = Split-Path -Parent $PSScriptRoot

$ErrorActionPreference = "Continue"
New-Item -ItemType Directory -Force -Path (Join-Path $RepoRoot "logs") | Out-Null
"frontend launcher started $(Get-Date -Format o)" | Out-File -FilePath (Join-Path $RepoRoot "logs\frontend.log") -Encoding utf8
$env:BROWSER = "none"
Set-Location (Join-Path $RepoRoot "frontend")
npm.cmd run dev -- --host 127.0.0.1 --port 5173 --strictPort 2>&1 | Tee-Object -FilePath (Join-Path $RepoRoot "logs\frontend.log") -Append
