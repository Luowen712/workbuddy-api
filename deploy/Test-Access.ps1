param([string]$GuiImage = 'workbuddy2api-gui:access-candidate')
$ErrorActionPreference = 'Stop'
$gatewayName = 'wb-access-gateway-qa'
$guiName = 'wb-access-gui-qa'
$existing = @(docker ps -a --format '{{.Names}}')
if ($existing -contains $gatewayName -or $existing -contains $guiName) {
    throw 'QA container names are already in use; refusing to replace them.'
}
$root = Join-Path $PSScriptRoot ('qa-access-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
$null = New-Item -ItemType Directory -Path (Join-Path $root 'auths') -Force
$configPath = Join-Path $root 'config.json'
$oldKey = 'qa-only-initial-key'
$fixture = @{ listen = ':7863'; api_key = $oldKey; auth_dir = '/qa/auths'; custom = @{ keep = $true } }
[IO.File]::WriteAllText($configPath, ($fixture | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))
$base = 'http://localhost:18787'
$origin = @{ Origin = $base }
$session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
function Api([string]$method, [string]$path, $body) {
    $args = @{ Method=$method; Uri="$base$path"; WebSession=$session; Headers=$origin; TimeoutSec=100 }
    if ($null -ne $body) { $args.Body=$body | ConvertTo-Json; $args.ContentType='application/json' }
    Invoke-RestMethod @args
}
function ModelStatus([string]$key) {
    (Invoke-WebRequest -Uri 'http://localhost:17863/v1/models' -Headers @{Authorization="Bearer $key"} -SkipHttpErrorCheck -TimeoutSec 10).StatusCode
}
try {
    $null = docker run -d --name $gatewayName --user 0:0 -p 127.0.0.1:17863:7863 --mount "type=bind,source=$root,target=/qa" --entrypoint /app/wb2api gateway-wb2api:latest -config /qa/config.json
    if ($LASTEXITCODE -ne 0) { throw 'Cannot start isolated gateway.' }
    $null = docker run -d --name $guiName --user 0:0 -p 127.0.0.1:18787:8787 --mount "type=bind,source=$root,target=/qa" -v /var/run/docker.sock:/var/run/docker.sock -e WBGUI_GATEWAY_URL=http://host.docker.internal:17863 -e WBGUI_CONFIG_FILE=/qa/config.json -e WBGUI_AUTH_DIR=/qa/auths -e WBGUI_BACKUP_DIR=/qa/backups -e WBGUI_USERNAME=qa-admin -e WBGUI_PASSWORD=qa-only-password-rotate-test -e WBGUI_DANGEROUS_OPS=true -e "WBGUI_CONTAINER=$gatewayName" $GuiImage
    if ($LASTEXITCODE -ne 0) { throw 'Cannot start isolated GUI.' }
    $ready = $false
    for ($i=0; $i -lt 30; $i++) {
        try {
            $null = Api POST '/api/login' @{username='qa-admin'; password='qa-only-password-rotate-test'}
            $ready = $true
            break
        } catch { Start-Sleep -Milliseconds 500 }
    }
    if (!$ready) { throw 'Isolated GUI did not become ready.' }
    $initial = Api GET '/api/access' $null
    if ($initial.status -ne 'active' -or $initial.api_key -ne $oldKey) { throw 'Initial key verification failed.' }
    $created = Api POST '/api/access' @{confirm=$true; expected_key=$oldKey}
    $newKey = $created.api_key
    if ($newKey.Length -ne 70 -or $newKey -eq $oldKey) { throw 'Invalid generated key.' }
    $pending = Api GET '/api/access' $null
    if ($pending.status -ne 'rejected') { throw 'Saved key incorrectly reported active before restart.' }
    if ((ModelStatus $oldKey) -ne 200 -or (ModelStatus $newKey) -ne 401) { throw 'Pre-restart key state incorrect.' }
    $saved = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    if (!$saved.custom.keep) { throw 'Rotation removed an unrelated configuration field.' }
    $null = Api POST '/api/system/restart' @{}
    $active = $false
    for ($i=0; $i -lt 20; $i++) {
        $status = Api GET '/api/access' $null
        if ($status.status -eq 'active') { $active=$true; break }
        Start-Sleep -Milliseconds 500
    }
    if (!$active) { throw 'New key did not activate after restart.' }
    if ((ModelStatus $oldKey) -ne 401 -or (ModelStatus $newKey) -ne 200) { throw 'Post-restart key state incorrect.' }
    $models = Api GET '/api/models' $null
    if ($null -eq $models.count) { throw 'GUI did not follow the new key.' }
    $null = Api POST '/api/logout' @{}
    Write-Output 'PASS: isolated key generation, pending state, preserved config, restart, old-key rejection, new-key authentication, GUI key reload.'
} finally {
    foreach ($name in @($guiName, $gatewayName)) {
        if (@(docker ps -a --format '{{.Names}}') -contains $name) {
            $null = docker rm -f $name
        }
    }
}
