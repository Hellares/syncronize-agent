# Compila el agente para Windows 64 bits.
#   .\build.ps1                       -> dist\syncronize-agent.exe (produccion), version "dev-<commit>"
#   .\build.ps1 -Version 0.2.0        -> version fija (la que se reparte)
#   .\build.ps1 -Beta                 -> dist\syncronize-agent-beta.exe (doble clic vincula contra beta)
param([string]$Version = "", [switch]$Beta)

$ErrorActionPreference = "Stop"
if (-not $Version) {
    $commit = (git rev-parse --short HEAD 2>$null)
    $Version = if ($commit) { "dev-$commit" } else { "dev" }
}
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
$env:GOTOOLCHAIN = "local"

go test ./...
if ($LASTEXITCODE -ne 0) { exit 1 }

$ldflags = "-s -w -X main.Version=$Version"
$salida = "dist\syncronize-agent.exe"
if ($Beta) {
    $ldflags += " -X main.ServidorPorDefecto=https://saas-beta.syncronize.net.pe/api"
    $salida = "dist\syncronize-agent-beta.exe"
}
# Icono y "Propiedades > Detalles" del .exe: go-winres arma un .syso que
# go build incrusta solo. Beta lleva el icono con la franja "BETA".
$icono = if ($Beta) { "cmd\syncronize-agent\icono\icono-beta.png" } else { "cmd\syncronize-agent\icono\icono.png" }
$verWin = if ($Version -match '^\d+\.\d+\.\d+$') { "$Version.0" } else { "0.0.0.0" }
$syso = "cmd\syncronize-agent\rsrc_windows_amd64.syso"
go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --out cmd\syncronize-agent\rsrc `
    --icon $icono --manifest cli --file-version $verWin --product-version $verWin `
    --file-description "Syncronize - Agente de monitoreo" --product-name "Syncronize Agent" `
    --copyright "Syncronize" --original-filename "syncronize-agent.exe"
if ($LASTEXITCODE -ne 0) { exit 1 }

# -s -w: sin tabla de simbolos (binario ~30 % mas chico).
go build -trimpath -ldflags "$ldflags" -o $salida .\cmd\syncronize-agent
$ok = $LASTEXITCODE -eq 0
# El .syso es de ESTE build (prod o beta): no queda para el proximo.
Remove-Item $syso -ErrorAction SilentlyContinue
if (-not $ok) { exit 1 }
Write-Host "$salida ($Version)"

# Instalador grafico (Inno Setup 6): dist\Syncronize-Agent-Setup[-beta]-<version>.exe.
# Sin Inno Setup instalado se salta (el .exe suelto sigue sirviendo).
$iscc = @($env:ISCC, "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe",
    "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe") |
    Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
if ($iscc -and $Version -match '^\d+\.\d+\.\d+$') {
    $defs = @("/DVersion=$Version")
    if ($Beta) { $defs += "/DBeta" }
    & $iscc /Q @defs instalador\syncronize-agent.iss
    if ($LASTEXITCODE -ne 0) { exit 1 }
    Write-Host "instalador: dist\Syncronize-Agent-Setup$(if ($Beta) { '-beta' })-$Version.exe"
} elseif (-not $iscc) {
    Write-Host "(sin Inno Setup: no se arma el instalador grafico)"
}
# TODO: firmar con el certificado de firma de codigo (signtool sign /fd sha256 ...)
#       antes de repartirlo: sin firma, SmartScreen muestra "Windows protegio su PC".
