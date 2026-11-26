# Install the latest coop release into %LOCALAPPDATA%\coop\bin and add it to the user PATH.
#   irm https://raw.githubusercontent.com/xpresstoparadise/coop-cli/main/scripts/install.ps1 | iex
$ErrorActionPreference = 'Stop'
$repo = 'xpresstoparadise/coop-cli'
$dir = if ($env:COOP_INSTALL_DIR) { $env:COOP_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'coop\bin' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }

$tag = $env:COOP_VERSION
if (-not $tag) { $tag = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name }
$name = "coop_$($tag.TrimStart('v'))_windows_$arch"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "downloading coop $tag for windows/$arch"
  $zip = Join-Path $tmp "$name.zip"
  Invoke-WebRequest "https://github.com/$repo/releases/download/$tag/$name.zip" -OutFile $zip
  $sums = (Invoke-WebRequest "https://github.com/$repo/releases/download/$tag/checksums.txt").Content
  $want = ($sums -split "`n" | Where-Object { $_ -match " $name\.zip$" }) -replace ' .*', ''
  $got = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
  if ($want -ne $got) { throw "checksum mismatch" }
  Expand-Archive $zip -DestinationPath $tmp
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  Copy-Item (Join-Path $tmp "$name\coop.exe") (Join-Path $dir 'coop.exe') -Force
} finally {
  Remove-Item -Recurse -Force $tmp
}
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $dir) {
  [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
  Write-Host "added $dir to your PATH (open a new terminal)"
}
$env:Path = "$env:Path;$dir"
Write-Host "installed $(Join-Path $dir 'coop.exe') ($(& (Join-Path $dir 'coop.exe') version))"
