param(
    [switch]$SkipBuild,
    [switch]$Truncate
)

$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
$exe  = Join-Path $root 'blogsearch.exe'
$dir  = Join-Path $root 'data'
$log  = Join-Path $dir 'blogsearch.log'

Push-Location $root
try {
    if (-not $SkipBuild) {
        & go build -o $exe .
        if ($LASTEXITCODE -ne 0) { throw "编译失败：$LASTEXITCODE" }
    }
    if (-not (Test-Path -LiteralPath $exe)) { throw "缺少文件：$exe" }

    Get-Process -Name 'blogsearch' -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Milliseconds 500

    if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Path $dir | Out-Null }
    if ($Truncate -and (Test-Path -LiteralPath $log)) { Remove-Item -LiteralPath $log -Force }

    try {
        [System.IO.File]::Open($log, 'Append', 'Write', 'None').Close()
    }
    catch {
        $log = Join-Path $dir ("blogsearch-{0}.log" -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
    }

    $utf8 = [System.Text.Encoding]::UTF8
    [Console]::OutputEncoding = $utf8
    $OutputEncoding           = $utf8

    Write-Host "日志：$log"
    Write-Host "停止：Ctrl+C"
    & $exe 2>&1 | Tee-Object -FilePath $log -Append
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
