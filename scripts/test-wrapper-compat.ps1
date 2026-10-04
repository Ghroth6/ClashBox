# Run the portable adapters against the pinned official core or an explicit candidate.
[CmdletBinding()]
param([string]$CoreRevision)

$ErrorActionPreference = 'Stop'
$coreOverride = $null
if ($PSBoundParameters.ContainsKey('CoreRevision')) {
  if ($CoreRevision -cnotmatch '^[0-9a-f]{40}$') {
    throw 'CoreRevision must be a full 40-character lowercase commit SHA'
  }
  $coreOverride = $CoreRevision
}
$app = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$project = [IO.Path]::GetFullPath((Join-Path $app '../..'))
$core = Join-Path $project 'sources/core'
[xml]$manifest = Get-Content -LiteralPath (Join-Path $project 'meta/default.xml') -Raw
$pin = @($manifest.manifest.project | Where-Object { $_.path -eq 'sources/core' })
if ($pin.Count -ne 1) { throw 'Project manifest must contain exactly one official core pin' }
$manifestCore = [string]$pin[0].revision
$head = git -C $core rev-parse HEAD
if ($LASTEXITCODE -ne 0) { throw 'Cannot read official core HEAD' }
if ($null -ne $coreOverride -and $head -cne $coreOverride) {
  throw 'Official core HEAD must match the explicit CoreRevision'
}
if ($null -eq $coreOverride -and $head -cne $manifestCore) {
  throw 'Official core HEAD must match the project manifest'
}
$dirty = @(git -C $core status --porcelain=v1 --untracked-files=all --ignore-submodules=none)
if ($LASTEXITCODE -ne 0) { throw 'Cannot check official core working tree' }
if ($dirty.Count -ne 0) {
  throw 'Official core working tree must be clean, including non-ignored untracked files'
}
$go = (Get-Command go -CommandType Application).Source
$version = & $go version
if ($LASTEXITCODE -ne 0 -or $version -notmatch 'go1\.24\.5 windows/amd64') {
  throw "Use the documented host Go 1.24.5 windows/amd64; found $version"
}
$batch = 'wrapper-compat-tests-' + (Get-Date -AsUTC -Format 'yyyyMMddTHHmmssfffffffZ')
$stage = Join-Path $app "local/$batch"
$source = Join-Path $app 'proxy_core/src/flclash/compat'
if (Test-Path -LiteralPath $stage) { throw 'Existing test evidence; choose a new batch' }
New-Item -ItemType Directory -Path (Join-Path $stage 'compat') | Out-Null
$hashes = [ordered]@{}
foreach ($file in Get-ChildItem -LiteralPath $source -File -Filter '*.go') {
  Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $stage 'compat')
  $hashes[$file.Name] = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash
}
@'
module core

go 1.24

require github.com/metacubex/mihomo v1.0.0
replace github.com/metacubex/mihomo => ../../../core
'@ | Set-Content -LiteralPath (Join-Path $stage 'go.mod') -Encoding utf8NoBOM
[ordered]@{core=$head;manifest_core=$manifestCore;core_revision_override=$coreOverride;go=$version;source_sha256=$hashes;scope='Portable adapters only; no full wrapper, OHOS build or device validation'} |
  ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $stage 'inputs.json') -Encoding utf8NoBOM
$env:GOENV = 'off'
$env:GOTOOLCHAIN = 'local'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
$env:GOPATH = Join-Path $app 'local/go-host'
$env:GOMODCACHE = Join-Path $app 'local/go-mod-cache'
$env:GOCACHE = Join-Path $app 'local/go-host-cache'
& $go -C $stage test -mod=mod -count=1 -timeout=90s -v ./compat 2>&1 |
  Tee-Object -FilePath (Join-Path $stage 'test.log')
$result = $LASTEXITCODE
[ordered]@{exit_code=$result;scope='Portable adapters only';log='test.log'} |
  ConvertTo-Json | Set-Content -LiteralPath (Join-Path $stage 'result.json') -Encoding utf8NoBOM
Write-Output "Evidence: $stage"
exit $result
