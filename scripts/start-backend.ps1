<#
.SYNOPSIS
  Restart the multimodal backend (app.exe) with all P0 env vars.

.DESCRIPTION
  Single source of truth for backend env vars. Kills any existing app.exe,
  sets MM_* / SERVER_PORT, and relaunches in the background.

  Override any var by passing -Env @{ KEY = 'value' } before invoking, or by
  pre-setting the var in the calling shell (existing values are preserved).

.PARAMETER Foreground
  Run app.exe in the foreground (Ctrl+C to stop) instead of detaching.

.PARAMETER NoKill
  Do not kill an already-running app.exe; abort if one exists.

.PARAMETER WaitForReady
  Block until http://127.0.0.1:$Port/health returns 200 (max 30s).

.PARAMETER SkipBuild
  Skip the `go build`. Only use when you KNOW app.exe matches the source —
  a stale binary makes e2e green against code you never wrote.

.EXAMPLE
  .\start-backend.ps1                    # default: kill+relaunch detached
  .\start-backend.ps1 -Foreground        # for interactive log tailing
  .\start-backend.ps1 -WaitForReady      # block until /health responds

.NOTES
  Edit the $Defaults hashtable below to change the canonical env values.
#>
[CmdletBinding()]
param(
    [switch]$Foreground,
    [switch]$NoKill,
    [switch]$WaitForReady,
    [switch]$SkipBuild,
    [int]$Port = 8280
)

# 仓库根由脚本自身位置推导 —— 别再写死绝对路径（上个仓库搬家时这里全炸了）。
$RepoRoot = Split-Path -Parent $PSScriptRoot

$ErrorActionPreference = 'Stop'

function Resolve-Ffmpeg([string]$Name) {
    $envVar = if ($Name -eq 'ffmpeg') { $Env:MM_FFMPEG_PATH } else { $Env:MM_FFPROBE_PATH }
    if ($envVar) { return $envVar }
    $local = Join-Path $RepoRoot (Join-Path 'tools' (Join-Path 'bin' "$Name.exe"))
    if (Test-Path $local) { return $local }
    if (Get-Command $Name -ErrorAction SilentlyContinue) { return $Name }
    Write-Warning "$Name 未找到：媒体派生（帧索引/缩略图）会失败，视频标注不可用。"
    Write-Warning '  装一个：winget install ffmpeg  ——或把二进制放进 tools/bin/'
    return $Name
}


# --- 本地密钥（不进 git）--------------------------------------------------
# 仓库是公开的：**任何密钥都不准写进被跟踪的文件**。
# 真实密钥放 scripts/secrets.local.ps1（被 .gitignore 挡着），照
# scripts/secrets.local.example.ps1 抄一份填进去即可。
$SecretsFile = Join-Path $PSScriptRoot 'secrets.local.ps1'
if (Test-Path $SecretsFile) {
    . $SecretsFile
}

# dev 的 JWT 密钥必须**稳定**：config.go 在密钥为空时每次启动都随机生成一把，
# 于是每重启一次后端，所有已登录会话全部失效（登录后一重启就被登出）。
# 但它同样不能硬编码进仓库 —— 所以：本地生成一次，存到 gitignore 掉的文件里。
if (-not $env:JWT_SECRET) {
    $JwtFile = Join-Path $PSScriptRoot '.jwt-secret.local'
    if (-not (Test-Path $JwtFile)) {
        $bytes = New-Object byte[] 32
        [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
        ([System.BitConverter]::ToString($bytes) -replace '-','').ToLower() |
            Out-File -FilePath $JwtFile -Encoding ascii -NoNewline
        Write-Host "[backend] 已生成本地 dev JWT 密钥 -> $JwtFile（不进 git）" -ForegroundColor DarkCyan
    }
    $env:JWT_SECRET = (Get-Content $JwtFile -Raw).Trim()
}

if (-not $env:MM_LITELLM_API_KEY) {
    Write-Warning "MM_LITELLM_API_KEY 未设置 —— VLM 调用会 401。请在 scripts/secrets.local.ps1 里填上。"
}

# 模型服务所在主机（103 GPU 机）。真实内网地址放本地密钥文件，别进公开仓库。
if (-not $env:MM_MODEL_HOST) { $env:MM_MODEL_HOST = '127.0.0.1' }
$ModelHost = $env:MM_MODEL_HOST

# --- Canonical env defaults (single source of truth) ----------------------
# Existing shell-level env vars take precedence; only unset vars get defaults.
$Defaults = [ordered]@{
    SERVER_PORT             = ":$Port"
    # APP_ENV=dev: 跳过 prod 的强 JWT_SECRET / ALLOWED_ORIGINS 强校验，并将
    # 允许来源默认放开到 localhost:5173（前端 dev 端口）。生产部署请在调用前
    # 预设 $env:APP_ENV='prod' 并提供强 JWT_SECRET + ALLOWED_ORIGINS。
    APP_ENV                 = 'dev'
    # 关系库 = PostgreSQL（唯一方言；schema 由 goose 迁移自动建出）。
    # 本地容器：docker run -d --name data_governance_postgres `
    #   -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=data_governance -p 5432:5432 postgres:16
    DATABASE_URL            = 'postgres://postgres:postgres@localhost:5432/data_governance?sslmode=disable'

    MM_OBJECT_STORE_DRIVER  = 'local'
    # 实际上传的 blob 落在 ./storage/assets（config.go 默认值）。务必与运行时
    # 工作目录 <repo>\backend 下的该路径一致，否则 AI 调用会报
    # "read asset: object not found"。
    MM_OBJECT_STORE_ROOT    = (Join-Path $RepoRoot "backend\storage\assets")

    # MinIO (切换驱动：将 MM_OBJECT_STORE_DRIVER 改为 'minio' 并填入以下变量)
    # MM_MINIO_ENDPOINT       = 'localhost:9000'
    # MM_MINIO_ACCESS_KEY     = 'minioadmin'
    # MM_MINIO_SECRET_KEY     = 'minioadmin'
    # MM_MINIO_BUCKET         = 'annotation-assets'
    # MM_MINIO_USE_SSL        = 'false'

    MM_WORKER_ENABLED       = 'false'    # 本地 AI 容器已清理，Worker 暂停（103 部署完成后可改回 true）
    MM_WORKER_INTERVAL      = '2s'
    MM_WORKER_BATCH         = '4'
    MM_WORKER_LEASE_TTL     = '60s'
    MM_AI_RETRY_MAX         = '2'
    MM_AI_TIMEOUT           = '120s'

    # A/V 流水线（M0/A1/A2）：Redis 编辑锁 + media-worker 派生（ffmpeg）+ FunASR ASR。
    # ⚠️ 用 db 1，与旧项目（db 0）隔离。
    # 共用 db 会撞上「幽灵去重」：SHA 缓存键是 asset:sha256:{datasetID}:{sha}，
    # 两个库的 dataset id 一撞，上传就会命中另一个库里根本不存在的资产，
    # 直接返回「已去重」而从不 INSERT —— 资产建不出来、任务也建不出来。
    REDIS_URL               = 'redis://localhost:6379/1'
    REDIS_DB                = '1'
    MM_MEDIA_WORKER_ENABLED = 'true'
    # ffmpeg/ffprobe 不随仓库分发（二进制不进 git）。解析顺序：
    #   1) 显式 MM_FFMPEG_PATH  2) 本地 tools/bin/  3) PATH
    # 没有它们 → 帧索引出不来 → 视频工作台总帧数为 0、跳帧全被 clamp 到 0。
    MM_FFMPEG_PATH          = (Resolve-Ffmpeg 'ffmpeg')
    MM_FFPROBE_PATH         = (Resolve-Ffmpeg 'ffprobe')
    MM_ASR_ENDPOINT         = "http://${ModelHost}:8390"   # asr-p0 容器（FunASR，103）；不可达时音频任务自动降级 HUMAN

    # LiteLLM (VLM gateway) - already validated in §11.5
    # v0.5: default model = qwen-vl-plus (A/B 实测 §7 实验 C：plus 速度 ~2.2x，
    # 质量与 max 近乎等价)。litellm-config.yaml 同时注册 qwen-vl-max 供 ad-hoc 选用。
    MM_LITELLM_ENDPOINT     = "http://${ModelHost}:4000"
    MM_LITELLM_API_KEY      = ''   # 真实值放 scripts/secrets.local.ps1（不进 git）
    MM_VLM_MODEL            = 'qwen-vl-plus'
    MM_LITELLM_CONFIG_PATH  = ''   # LiteLLM 在 103 容器内读自己的 config，本地改这份文件不生效

    # PaddleOCR (Phase 1 - container ocr-p0)
    MM_OCR_ENDPOINT         = 'http://127.0.0.1:8400'
    MM_OCR_API_KEY          = ''

    # YOLOv8-seg instance segmentation (container seg-p0, capability seg.instance)
    MM_SEG_ENDPOINT         = 'http://127.0.0.1:8500'
    MM_SEG_API_KEY          = ''

    # SAM 2.1 interactive segmentation (容器 sam-p0:2.1，capability seg.interactive)
    # 跑在 103 GPU 服务器；图片=分割静态图，视频=前端传当前帧 image_b64（点选→多边形关键帧）。
    MM_SAM_ENDPOINT         = "http://${ModelHost}:8381"
    MM_SAM_API_KEY          = ''

    # det-server 检测+追踪（容器 det-p0，capability video.detect_track，B2）——
    # 跑在 103 GPU 服务器（YOLO26x + ByteTrack/BoT-SORT）。端点只要非空即注册 adapter；
    # 不可达时手动触发 detect-track 会报错降级，不影响其它模态。
    MM_DET_ENDPOINT         = "http://${ModelHost}:8382"
    MM_DET_API_KEY          = ''

    # sam2-video 跨帧传播（容器 sam2-video:1.0，capability video.sam2_propagate，B2.2）——
    # 点选一帧物体 → SAM2 传播全片 → 整条 mask track。跑在 103:8384。
    MM_SAM2_ENDPOINT        = "http://${ModelHost}:8384"
    MM_SAM2_API_KEY         = ''

    # qwen-audio 整段转写（容器 qwen-audio-p0，capability audio.transcribe，Qwen2.5-Omni-7B）——
    # 跑在 103:8383。返回整段纯文本，包成一个整段 ASR region，标注员再切分。
    MM_AUDIO_ENDPOINT       = "http://${ModelHost}:8383"
    MM_AUDIO_API_KEY        = ''
}

foreach ($k in $Defaults.Keys) {
    $existing = [Environment]::GetEnvironmentVariable($k, 'Process')
    if ([string]::IsNullOrEmpty($existing)) {
        Set-Item -Path "env:$k" -Value $Defaults[$k]
    }
}

$AppPath = (Join-Path $RepoRoot "backend\app.exe")

# 每次启动都重新编译。以前这里只检查 app.exe 存不存在——**不检查它是不是旧的**，
# 于是改完 Go 代码直接跑这个脚本，起来的还是上一次编译的二进制：后端单测是绿的
# （它们编译当前源码），e2e 也是绿的（它打的是旧二进制），**而你的改动根本没被跑过**。
# 这是「绿色可以是假的」最阴的一种：三层测试全绿，测的却不是你写的代码。
# 编译很快（有 build cache），不值得为省这几秒留这么大一个坑。
if (-not $SkipBuild) {
    Write-Host "[backend] Building app.exe from source ..." -ForegroundColor DarkCyan
    Push-Location (Join-Path $RepoRoot "backend")
    try {
        & go build -o app.exe ./cmd
        if ($LASTEXITCODE -ne 0) { throw "go build failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
}
if (-not (Test-Path $AppPath)) {
    throw "app.exe not found at $AppPath. Build it first (cd backend; go build -o app.exe ./cmd)."
}

# --- Kill any existing instance --------------------------------------------
$existing = Get-CimInstance Win32_Process -Filter "Name='app.exe'" |
    Where-Object { $_.ExecutablePath -ieq $AppPath }
if ($existing) {
    if ($NoKill) {
        throw "app.exe already running (PID=$($existing.ProcessId)); pass -NoKill:`$false to replace it."
    }
    foreach ($p in $existing) {
        Write-Host "[backend] Stopping existing app.exe (PID=$($p.ProcessId)) ..." -ForegroundColor DarkYellow
        try { Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop } catch { Write-Warning $_ }
    }
    Start-Sleep -Milliseconds 800
}

# Also free the port from ANY squatter (e.g. a leftover `go run ./cmd` temp
# cmd.exe), otherwise the fresh app.exe silently fails to bind and exits while
# requests keep hitting the stale instance.
$portOwner = (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue).OwningProcess
if ($portOwner) {
    $owner = Get-Process -Id $portOwner -ErrorAction SilentlyContinue
    Write-Host "[backend] Freeing port $Port held by PID=$portOwner ($($owner.ProcessName)) ..." -ForegroundColor DarkYellow
    try { Stop-Process -Id $portOwner -Force -ErrorAction Stop; Start-Sleep -Milliseconds 600 } catch { Write-Warning $_ }
}

# --- Print resolved env ----------------------------------------------------
Write-Host "[backend] Resolved env:" -ForegroundColor Cyan
foreach ($k in $Defaults.Keys) {
    $v = [Environment]::GetEnvironmentVariable($k, 'Process')
    if ($k -match 'KEY|SECRET' -and $v) {
        $masked = if ($v.Length -gt 8) { $v.Substring(0,4) + '***' + $v.Substring($v.Length-4) } else { '***' }
        Write-Host ("  {0,-24} = {1}" -f $k, $masked) -ForegroundColor DarkGray
    } else {
        Write-Host ("  {0,-24} = {1}" -f $k, $v) -ForegroundColor DarkGray
    }
}

# --- Launch ----------------------------------------------------------------
$workDir = Split-Path -Parent $AppPath
if ($Foreground) {
    Write-Host "[backend] Launching in foreground (Ctrl+C to stop) ..." -ForegroundColor Green
    Push-Location $workDir
    try { & $AppPath } finally { Pop-Location }
    return
}

Write-Host "[backend] Launching detached at $AppPath ..." -ForegroundColor Green
$logPath = Join-Path $workDir 'app.log'
$proc = Start-Process -FilePath $AppPath -WorkingDirectory $workDir `
    -RedirectStandardOutput $logPath `
    -RedirectStandardError (Join-Path $workDir 'app.err.log') `
    -PassThru -WindowStyle Hidden
Write-Host "[backend] PID=$($proc.Id) logs=$logPath"

if ($WaitForReady) {
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/auth/login" -Method Post `
                -Body '{}' -ContentType 'application/json' -UseBasicParsing -TimeoutSec 2 `
                -ErrorAction Stop
        } catch {
            # any HTTP response (including 4xx) means the port is up
            if ($_.Exception.Response) { Write-Host "[backend] /auth/login responding ($($_.Exception.Response.StatusCode)) - ready"; break }
        }
        Start-Sleep -Milliseconds 500
    }
    if ((Get-Date) -ge $deadline) {
        Write-Warning "[backend] readiness probe timed out; check $logPath"
    }
}
