[CmdletBinding()]
param(
    [string]$OutputDir = ".\releases",
    [ValidateSet("linux")]
    [string]$GoOs = "linux",
    [ValidateSet("amd64", "arm64")]
    [string]$GoArch = "amd64",
    [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Require-Command {
    param([Parameter(Mandatory = $true)][string]$Name)

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Missing required command: $Name"
    }
}

function New-CleanDirectory {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (Test-Path -LiteralPath $Path) {
        Remove-Item -LiteralPath $Path -Recurse -Force
    }
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
}

function Copy-IfExists {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    if (Test-Path -LiteralPath $Source) {
        Copy-Item -LiteralPath $Source -Destination $Destination -Recurse -Force
    }
}

function Compress-Directory {
    param(
        [Parameter(Mandatory = $true)][string]$SourceDir,
        [Parameter(Mandatory = $true)][string]$ZipPath
    )

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    if (Test-Path -LiteralPath $ZipPath) {
        Remove-Item -LiteralPath $ZipPath -Force
    }

    $archive = [System.IO.Compression.ZipFile]::Open($ZipPath, [System.IO.Compression.ZipArchiveMode]::Create)
    try {
        $sourceRoot = (Resolve-Path -LiteralPath $SourceDir).Path
        $rootLength = $sourceRoot.Length + 1
        Get-ChildItem -LiteralPath $sourceRoot -Recurse -File | ForEach-Object {
            $relativePath = $_.FullName.Substring($rootLength).Replace('\', '/')
            [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
                $archive,
                $_.FullName,
                $relativePath,
                [System.IO.Compression.CompressionLevel]::Optimal
            ) | Out-Null
        }
    }
    finally {
        $archive.Dispose()
    }
}

function Ensure-FrontendBuild {
    param(
        [Parameter(Mandatory = $true)][string]$FrontendDir,
        [Parameter(Mandatory = $true)][string]$Version
    )

    Push-Location $FrontendDir
    try {
        if (-not (Test-Path -LiteralPath (Join-Path $FrontendDir "node_modules"))) {
            bun install --frozen-lockfile
        }

        $previousDisable = $env:DISABLE_ESLINT_PLUGIN
        $previousVersion = $env:VITE_REACT_APP_VERSION
        $env:DISABLE_ESLINT_PLUGIN = "true"
        $env:VITE_REACT_APP_VERSION = $Version

        try {
            bun run build
        }
        finally {
            $env:DISABLE_ESLINT_PLUGIN = $previousDisable
            $env:VITE_REACT_APP_VERSION = $previousVersion
        }
    }
    finally {
        Pop-Location
    }
}

$repoRoot = Split-Path -Parent $PSCommandPath
$outputRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $OutputDir))
$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$bundleName = "new-api-$GoOs-$GoArch-$timestamp"
$stagingDir = Join-Path $outputRoot $bundleName
$zipPath = Join-Path $outputRoot "$bundleName.zip"
$binaryPath = Join-Path $stagingDir "new-api"
$versionPath = Join-Path $repoRoot "VERSION"
$version = ""

if (Test-Path -LiteralPath $versionPath) {
    $versionContent = Get-Content -LiteralPath $versionPath -Raw
    if ($null -ne $versionContent) {
        $version = $versionContent.Trim()
    }
}

Require-Command "go"
Require-Command "bun"

if ([string]::IsNullOrWhiteSpace($version)) {
    $version = "dev"
}

if ((Test-Path -LiteralPath $zipPath) -and -not $Force) {
    throw "Package already exists: $zipPath`nRe-run with -Force to overwrite."
}

New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null
New-CleanDirectory -Path $stagingDir

Ensure-FrontendBuild -FrontendDir (Join-Path $repoRoot "web/default") -Version $version
Ensure-FrontendBuild -FrontendDir (Join-Path $repoRoot "web/classic") -Version $version

$previousEnv = @{
    CGO_ENABLED = $env:CGO_ENABLED
    GOOS        = $env:GOOS
    GOARCH      = $env:GOARCH
}

Push-Location $repoRoot
try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = $GoOs
    $env:GOARCH = $GoArch

    go build -trimpath -ldflags="-s -w" -o $binaryPath .\main.go
}
finally {
    $env:CGO_ENABLED = $previousEnv.CGO_ENABLED
    $env:GOOS = $previousEnv.GOOS
    $env:GOARCH = $previousEnv.GOARCH
    Pop-Location
}

Copy-IfExists -Source (Join-Path $repoRoot ".env") -Destination $stagingDir
Copy-IfExists -Source (Join-Path $repoRoot ".env.example") -Destination $stagingDir
Copy-IfExists -Source (Join-Path $repoRoot "README.zh_CN.md") -Destination $stagingDir
Copy-IfExists -Source (Join-Path $repoRoot "new-api.service") -Destination $stagingDir
Copy-IfExists -Source $versionPath -Destination $stagingDir

$dataDir = Join-Path $stagingDir "data"
$logsDir = Join-Path $stagingDir "logs"
New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
New-Item -ItemType Directory -Path $logsDir -Force | Out-Null
Set-Content -LiteralPath (Join-Path $dataDir ".keep") -Value "" -NoNewline
Set-Content -LiteralPath (Join-Path $logsDir ".keep") -Value "" -NoNewline

$manifest = @(
    "new-api"
    ".env"
    ".env.example"
    "README.zh_CN.md"
    "new-api.service"
    "VERSION"
    "data/"
    "logs/"
) -join [Environment]::NewLine
Set-Content -LiteralPath (Join-Path $stagingDir "package-manifest.txt") -Value $manifest -NoNewline

Compress-Directory -SourceDir $stagingDir -ZipPath $zipPath
Remove-Item -LiteralPath $stagingDir -Recurse -Force

Write-Host "Created package: $zipPath"
