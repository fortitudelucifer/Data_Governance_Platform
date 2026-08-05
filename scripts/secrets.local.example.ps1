# 本地密钥模板 —— 复制为 secrets.local.ps1 后填入真实值。
#
#     Copy-Item scripts\secrets.local.example.ps1 scripts\secrets.local.ps1
#
# secrets.local.ps1 被 .gitignore 挡着，**永远不会进仓库**。
# 这个仓库是公开的：任何真实密钥写进被跟踪的文件，就等于公开发布。
#
# start-backend.ps1 会在设置 env 默认值之前 dot-source 这个文件。

# LiteLLM 网关的 master key（不是 DashScope 的 key —— DashScope 的 key 在 103 容器的
# 环境变量 DASHSCOPE_API_KEY 里，不经过本仓库）。
$env:MM_LITELLM_API_KEY = 'sk-替换成你自己的-litellm-master-key'

# 模型服务所在主机（103 GPU 机的内网地址）。不填则默认 127.0.0.1，
# 所有 103 上的能力（VLM / SAM / SAM2 / 检测追踪 / ASR）都会连不上。
$env:MM_MODEL_HOST = '192.168.x.x'

# JWT 签名密钥。留空则 start-backend.ps1 会自动生成一把，存到
# scripts/.jwt-secret.local（同样不进 git），dev 下保持稳定即可。
# $env:JWT_SECRET = ''
