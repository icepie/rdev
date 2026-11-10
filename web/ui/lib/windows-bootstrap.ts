/** Win7's default PowerShell runs on CLR 2, whose WebClient cannot select TLS 1.2. */
export function legacyWindowsBootstrap(url: string): string {
  const quotedUrl = url.replaceAll("'", "''");
  return `try { $r=New-Object -ComObject WinHttp.WinHttpRequest.5.1; $r.SetTimeouts(15000,15000,30000,120000); $r.Open('GET','${quotedUrl}',$false); $r.Option(9)=2048; $r.Option(12)=$false; $r.Send(); if ($r.Status -ne 200) { throw ('HTTP '+$r.Status) }; $script=$r.ResponseText } catch { throw ('RDev download failed. Windows 7 requires SP1 and TLS 1.2 support. Check Windows updates, certificates and network. '+$_.Exception.Message) }; $script | iex`;
}
