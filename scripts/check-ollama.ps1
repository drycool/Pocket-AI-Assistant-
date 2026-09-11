param([string]$HostIP = "192.168.11.111")

# Проверка TCP-доступности порта Ollama (11434).
# Возвращает exit 0 - порт открыт, exit 1 - недоступен.
# ASCII-only: без проблем с кодировками на любой Windows.

$port = 11434
$c = New-Object Net.Sockets.TcpClient
try {
    $iar = $c.BeginConnect($HostIP, $port, $null, $null)
    if ($iar.AsyncWaitHandle.WaitOne(2500)) {
        $c.EndConnect($iar)
        $c.Close()
        exit 0
    }
    $c.Close()
    exit 1
} catch {
    $c.Close()
    exit 1
}
