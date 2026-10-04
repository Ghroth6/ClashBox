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
$manifestPath = Join-Path $project 'meta/default.xml'
[xml]$manifest = Get-Content -LiteralPath $manifestPath -Raw
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
$appHead = git -C $app rev-parse HEAD
if ($LASTEXITCODE -ne 0) { throw 'Cannot read application HEAD' }
$appInputsStatus = @(git -C $app status --porcelain=v1 --untracked-files=all -- 'scripts/test-wrapper-compat.ps1' 'proxy_core/src/flclash/compat')
if ($LASTEXITCODE -ne 0) { throw 'Cannot check application test inputs' }
$go = (Get-Command go -CommandType Application).Source
$version = & $go version
if ($LASTEXITCODE -ne 0 -or $version -notmatch 'go1\.24\.5 windows/amd64') {
  throw "Use the documented host Go 1.24.5 windows/amd64; found $version"
}
$batch = 'wrapper-compat-tests-' + (Get-Date -AsUTC -Format 'yyyyMMddTHHmmssfffffffZ')
$stage = Join-Path $app "local/runs/$batch"
$source = Join-Path $app 'proxy_core/src/flclash/compat'
if (Test-Path -LiteralPath $stage) { throw 'Existing test evidence; choose a new batch' }
New-Item -ItemType Directory -Path (Join-Path $stage 'compat') | Out-Null
$hashes = [ordered]@{}
foreach ($file in Get-ChildItem -LiteralPath $source -File -Filter '*.go') {
  Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $stage 'compat')
  $hashes[$file.Name] = (Get-FileHash -LiteralPath (Join-Path $stage "compat/$($file.Name)") -Algorithm SHA256).Hash
}
@'
module core

go 1.24

require github.com/metacubex/mihomo v1.0.0
replace github.com/metacubex/mihomo => ../../../../core
'@ | Set-Content -LiteralPath (Join-Path $stage 'go.mod') -Encoding utf8NoBOM
Copy-Item -LiteralPath (Join-Path $core 'go.sum') -Destination (Join-Path $stage 'go.sum')
$inputHashes = [ordered]@{
  'app/scripts/test-wrapper-compat.ps1' = (Get-FileHash -LiteralPath $PSCommandPath -Algorithm SHA256).Hash
  'meta/default.xml' = (Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash
  'core/go.mod' = (Get-FileHash -LiteralPath (Join-Path $core 'go.mod') -Algorithm SHA256).Hash
  'core/go.sum' = (Get-FileHash -LiteralPath (Join-Path $core 'go.sum') -Algorithm SHA256).Hash
}
$preparedHashes = [ordered]@{}
foreach ($name in @('go.mod', 'go.sum')) {
  $preparedHashes[$name] = (Get-FileHash -LiteralPath (Join-Path $stage $name) -Algorithm SHA256).Hash
}
[ordered]@{app=$appHead;app_inputs_status=$appInputsStatus;core=$head;manifest_core=$manifestCore;core_revision_override=$coreOverride;go=$version;input_sha256=$inputHashes;source_sha256=$hashes;prepared_sha256=$preparedHashes;scope='Portable adapters only; no full wrapper, OHOS build or device validation'} |
  ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $stage 'inputs.json') -Encoding utf8NoBOM
$env:GOENV = 'off'
$env:GOTOOLCHAIN = 'local'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
$env:GOPATH = Join-Path $app 'local/cache/go-host'
$env:GOMODCACHE = Join-Path $app 'local/cache/go-mod-cache'
$env:GOCACHE = Join-Path $app 'local/cache/go-host-cache'
& $go -C $stage test -mod=mod -count=1 -timeout=90s -v ./compat 2>&1 |
  Tee-Object -FilePath (Join-Path $stage 'test.log')
$result = $LASTEXITCODE
$resolvedHashes = [ordered]@{}
foreach ($name in @('go.mod', 'go.sum')) {
  $resolvedHashes[$name] = (Get-FileHash -LiteralPath (Join-Path $stage $name) -Algorithm SHA256).Hash
}
[ordered]@{exit_code=$result;scope='Portable adapters only';inputs='inputs.json';log='test.log';resolved_inputs_sha256=$resolvedHashes} |
  ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $stage 'result.json') -Encoding utf8NoBOM
Write-Output "Evidence: $stage"
exit $result
