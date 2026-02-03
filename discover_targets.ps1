param(
  [int]$TimeoutMs = 2000
)

$ErrorActionPreference = "Stop"

# --- 1) Visual Studio DTE (VS 2022) starten/holen ---
$dte = $null
try {
  $dte = [Runtime.InteropServices.Marshal]::GetActiveObject("VisualStudio.DTE.17.0")
} catch {
  $dte = New-Object -ComObject "VisualStudio.DTE.17.0"
}

# möglichst "headless"
try { $dte.MainWindow.Visible = $false } catch {}
try { $dte.UserControl = $false } catch {}

# --- 2) TwinCAT XAE / SystemManager bekommen ---
# Beckhoff AI läuft typischerweise über ein TwinCAT-Projekt (XAE).
# Wir erzeugen eine temporäre Solution/Projektstruktur in %TEMP%.
$tempRoot = Join-Path $env:TEMP "tc_ai_discovery"
New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null

$solutionPath = Join-Path $tempRoot "Discovery.sln"

# Falls schon offen: schließen
try {
  if ($dte.Solution -and $dte.Solution.IsOpen) { $dte.Solution.Close($false) }
} catch {}

# Neue Solution anlegen
$dte.Solution.Create($tempRoot, "Discovery")

# --- 3) TwinCAT XAE Projekt hinzufügen ---
# Hier ist der "knifflige" Teil: die Projekt-Template-ID hängt von Installation ab.
# Viele Systeme haben ein TwinCAT XAE Template. Wir versuchen, ein TwinCAT-Projekt Template zu finden.
# Falls das nicht klappt, musst du uns 1x die Template-ID nennen (über Tools->Templates).
$templatePath = $null

# Suche nach gängigen TwinCAT Templates
$possible = @(
  "TwinCAT XAE Project",
  "TwinCAT Project",
  "TcXaeProject"
)

# Templates über DTE sind je nach Setup schwer "sauber" zu enumerieren.
# Daher Workaround: Wir nutzen ein vorhandenes TwinCAT Projekt, falls du eins angibst.
# -> Wenn kein Projekt vorhanden, schlagen wir hier bewusst fehl mit klarer Fehlermeldung.
$existingTsproj = Join-Path $tempRoot "Discovery.tsproj"
if (-not (Test-Path $existingTsproj)) {
  throw "Kein TwinCAT Projekt vorhanden. Lege bitte ein minimales TwinCAT XAE Projekt (Discovery.tsproj) in $tempRoot an oder gib uns den Pfad zu einem bestehenden .tsproj."
}

$proj = $dte.Solution.AddFromFile($existingTsproj, $false)

# SystemManager Objekt aus Projekt
$systemManager = $proj.Object

# --- 4) Broadcast Search via TIRR ---
$routesItem = $systemManager.LookupTreeItem("TIRR")

$xmlReq = @"
<TreeItem>
  <RoutePrj>
    <TargetList>
      <BroadcastSearch>true</BroadcastSearch>
    </TargetList>
  </RoutePrj>
</TreeItem>
"@

$routesItem.ConsumeXml($xmlReq)

Start-Sleep -Milliseconds $TimeoutMs

# Ergebnis holen
$xmlOut = $routesItem.ProduceXml()

# --- 5) XML parsen und IP->NetID map bauen ---
[xml]$doc = $xmlOut
$map = @{}

# Je nach TwinCAT-Version heißen die Knoten minimal anders.
# Wir suchen robust nach allen Nodes, die IpAddr und NetId besitzen.
$targets = $doc.SelectNodes("//Target")
foreach ($t in $targets) {
  $ip = $t.IpAddr
  $netid = $t.NetId
  if ($ip -and $netid) {
    $map[$ip] = $netid
  }
}

# JSON ausgeben (nur Map)
$map | ConvertTo-Json -Compress
