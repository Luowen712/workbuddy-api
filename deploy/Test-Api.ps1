param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\gateway\config.json'),
    [string]$Base = 'http://localhost:7863'
)
$ErrorActionPreference = 'Stop'
$config = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
if ([string]::IsNullOrWhiteSpace($config.api_key)) { throw "api_key is not set in $ConfigPath" }
$headers = @{ Authorization = "Bearer $($config.api_key)" }
$models = Invoke-RestMethod "$base/v1/models" -Headers $headers
if ('cn:fast-model' -notin $models.data.id) { throw 'Expected test model is absent' }
$results = @([pscustomobject]@{ Test = 'Models'; HTTP = 200; Models = $models.data.Count })
foreach ($stream in @($false, $true)) {
    $body = @{
        model = 'cn:fast-model'
        messages = @(@{ role = 'user'; content = 'Reply with OK only.' })
        stream = $stream
        max_tokens = 256
    } | ConvertTo-Json -Depth 5
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $response = Invoke-WebRequest "$base/v1/chat/completions" -Headers $headers -Method Post -ContentType application/json -Body $body -TimeoutSec 100
    $watch.Stop()
    if ($stream) {
        $frames = @($response.Content -split '\r?\n' | Where-Object { $_.StartsWith('data: ') })
        if ('data: [DONE]' -notin $frames) { throw 'SSE terminal marker missing' }
        $content = ''
        foreach ($line in $frames) {
            $payload = $line.Substring(6)
            if ($payload -eq '[DONE]') { continue }
            $frame = $payload | ConvertFrom-Json
            if ($null -ne $frame.error) { throw 'Upstream SSE error frame received' }
            foreach ($choice in $frame.choices) { $content += $choice.delta.content }
        }
        if ([string]::IsNullOrWhiteSpace($content)) { throw 'SSE response is empty' }
        $results += [pscustomobject]@{ Test = 'Streaming'; HTTP = [int]$response.StatusCode; Milliseconds = $watch.ElapsedMilliseconds; Reply = $content; Frames = $frames.Count; Done = $true }
    } else {
        $data = $response.Content | ConvertFrom-Json
        $content = $data.choices[0].message.content
        if ([string]::IsNullOrWhiteSpace($content) -or $null -ne $data.error) { throw 'Chat response is empty or contains an error' }
        $results += [pscustomobject]@{ Test = 'Chat'; HTTP = [int]$response.StatusCode; Milliseconds = $watch.ElapsedMilliseconds; Reply = $content; Tokens = $data.usage.total_tokens }
    }
}
try {
    $null = Invoke-WebRequest "$base/v1/models" -Headers @{ Authorization = 'Bearer invalid-test-key' }
    throw 'Invalid API key was accepted'
} catch {
    if ([int]$_.Exception.Response.StatusCode -ne 401) { throw }
    $results += [pscustomobject]@{ Test = 'InvalidKey'; HTTP = 401 }
}
$results | ConvertTo-Json -Depth 5
