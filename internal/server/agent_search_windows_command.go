package server

import (
	"encoding/base64"
	"fmt"
	"unicode/utf16"
)

// windowsSearchCommand keeps user data out of both the device shell syntax and
// PowerShell source. Base64 arguments also avoid smart-quote parsing and wildcard
// interpretation of directory names. The script works with Windows PowerShell.
func windowsSearchCommand(pattern, path string, contents bool) string {
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$root=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))
$pattern=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))
try {
[Console]::OutputEncoding=New-Object Text.UTF8Encoding -ArgumentList $false
$item=Get-Item -LiteralPath $root -Force
`, base64.StdEncoding.EncodeToString([]byte(path)), base64.StdEncoding.EncodeToString([]byte(pattern)))
	if contents {
		script += `$found=$false
$regex=New-Object Text.RegularExpressions.Regex -ArgumentList $pattern
if ($item.PSIsContainer) { $files=Get-ChildItem -LiteralPath $item.FullName -Recurse -Force | Where-Object { -not $_.PSIsContainer } } else { $files=@($item) }
foreach ($file in $files) {
  $lineNumber=0
  Get-Content -LiteralPath $file.FullName -Encoding UTF8 | ForEach-Object {
    $lineNumber++
    if ($regex.IsMatch($_)) { $found=$true; '{0}:{1}:{2}' -f $file.FullName,$lineNumber,$_ }
  }
}
if (-not $found) { exit 1 }
`
	} else {
		script += `$wildcard=New-Object System.Management.Automation.WildcardPattern -ArgumentList $pattern,([System.Management.Automation.WildcardOptions]::IgnoreCase)
if (-not $item.PSIsContainer) { throw 'Search path must be a directory' }
Get-ChildItem -LiteralPath $item.FullName -Recurse -Force | Where-Object { $wildcard.IsMatch($_.Name) } | ForEach-Object { $_.FullName }
`
	}
	script += "} catch { [Console]::Error.WriteLine($_.Exception.Message); exit 2 }\n"
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, len(units)*2)
	for i, unit := range units {
		encoded[2*i] = byte(unit)
		encoded[2*i+1] = byte(unit >> 8)
	}
	return "powershell.exe -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(encoded)
}
