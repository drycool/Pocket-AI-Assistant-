param([string]$Config = "")

# Печатает ТОЛЬКО host публичного эндпоинта из Data\config.public.json.
# Единый источник истины: чтобы сменить публичный адрес (IP или DDNS-имя),
# правим ТОЛЬКО Data\config.public.json, а не check-ollama.bat.
# hostname тоже поддерживается: check-ollama.ps1 резолвит имя через DNS.
# Пустой вывод + exit 1 - файла нет / JSON битый / нет base_url.
# ASCII-only: без проблем с кодировками на любой Windows.
#
# Использование:
#   powershell -NoProfile -File get-public-host.ps1 -Config "D:\admin-stick\Data\config.public.json"
# Вывод: 178.159.218.240

if (-not $Config -or -not (Test-Path $Config)) { exit 1 }

try {
    $json = Get-Content -Raw -Path $Config | ConvertFrom-Json
} catch {
    exit 1
}

$base = $null
foreach ($p in $json.providers) {
    if ($p.base_url) { $base = $p.base_url; break }
}
if (-not $base) { exit 1 }

# http://host:port/v1  ->  host
$base = $base -replace '^[a-zA-Z]+://', ''
$base = ($base -split '/')[0]
$base = ($base -split ':')[0]
if (-not $base) { exit 1 }

Write-Output $base
exit 0
