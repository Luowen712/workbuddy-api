param(
    [switch]$Rotate,
    # 共享凭证与渲染产物目录，默认本脚本所在目录
    [string]$Root = $PSScriptRoot,
    # 网关 config.json 路径，默认 <仓库>/gateway/config.json
    [string]$GatewayConfig = (Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) 'gateway\config.json'),
    # 对外分享地址（Tailscale Funnel / 自建反代），仅用于生成 client-access.json
    [string]$PublicBaseUrl = 'https://your-share-domain.example.com'
)
$ErrorActionPreference = 'Stop'
$private = Join-Path $Root 'private'
$null = New-Item -ItemType Directory -Force -Path $private, (Join-Path $Root 'tailscale')
$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
& icacls.exe $Root /inheritance:r /grant:r "${identity}:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Could not secure sharing directory' }
$keyPath = Join-Path $private 'share-key.json'
if (!(Test-Path -LiteralPath $keyPath) -or $Rotate) {
    $bytes = New-Object byte[] 32
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    $key = 'sk-share-' + [Convert]::ToHexString($bytes).ToLowerInvariant()
    $secret = @{ api_key = $key; created_at = [DateTimeOffset]::Now.ToString('o') }
    [IO.File]::WriteAllText($keyPath, ($secret | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
}
$key = (Get-Content -LiteralPath $keyPath -Raw | ConvertFrom-Json).api_key
$gatewayKey = (Get-Content -LiteralPath $GatewayConfig -Raw | ConvertFrom-Json).api_key
if ($key -notmatch '^[A-Za-z0-9_.-]{32,}$' -or $gatewayKey -notmatch '^[A-Za-z0-9_.-]{16,}$' -or $key -eq $gatewayKey) {
    throw 'Missing or unsupported API key format; no proxy configuration was written'
}
$template = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'nginx.conf.template') -Raw
$rendered = $template.Replace('__SHARE_KEY__', $key).Replace('__GATEWAY_KEY__', $gatewayKey)
[IO.File]::WriteAllText((Join-Path $private 'nginx.conf'), $rendered, [Text.UTF8Encoding]::new($false))
$client = @{
    baseURL = $PublicBaseUrl
    api = 'openai-responses'
    apiKey = $key
    model = 'cn:deepseek-v4.1-flash'
}
[IO.File]::WriteAllText((Join-Path $private 'client-access.json'), ($client | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
Write-Output 'Sharing key/config ready. Secrets were not printed. Reload the proxy to apply.'
