// quick.go — быстрые проверки: десяток фактов за один заход, для головы и агента.
//
// Зачем это отдельно от diag. diag — это подробный снимок «как в systeminfo»:
// systeminfo, ipconfig /all, все диски, журнал событий. Он нужен, когда уже
// понятно, куда смотреть, и он дорог: клиент ждёт минуту-две, а модель получает
// килобайты, из которых для первого шага нужны десять строк. quick отвечает на
// вопросы, которые задаются ВСЕГДА: кто это, сколько памяти и диска, что
// слушает, куда установлены соединения, есть ли ошибки за сутки, включена ли
// защита, не ждёт ли машина перезагрузки.
//
// Формат — key=value, по одному факту на строку. Это не украшение: так вывод
// читает и модель (без парсера и без «догадайся, где тут число»), и оператор,
// и grep. Один заход, короткий таймаут, только чтение и только штатные средства
// системы — на чужом ноутбуке нечего устанавливать.
package main

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	// quickTimeout — потолок на весь снимок. Если машина настолько занята, что
	// не отдала и этого, честнее сказать «не успели», чем держать сессию.
	quickTimeout = 20 * time.Second
	// quickValueMax — одно поле не должно съедать контекст модели.
	quickValueMax = 240
	// quickRawMax — сколько сырого вывода показать, если полей не нашлось.
	quickRawMax = 600
)

// quickCommand — ОДНА команда, которая печатает только строки key=value.
// Одна, а не десять: каждый запуск powershell.exe — это почти секунда, и десять
// проверок по отдельности превратились бы в «быстро» на словах.
func quickCommand() string {
	if runtime.GOOS == "windows" {
		return quickWindows
	}
	return quickLinux
}

// quickWindows — снимок для Windows. Ошибки глушим на уровне скрипта
// (SilentlyContinue): отсутствие, скажем, Get-MpComputerStatus на серверной
// редакции — это не сбой проверки, а «этого здесь нет», и одна недоступная
// строка не должна убивать остальные.
const quickWindows = `$ErrorActionPreference='SilentlyContinue'
"host=$env:COMPUTERNAME"
"user=$env:USERNAME"
$os = Get-CimInstance Win32_OperatingSystem
"os=$($os.Caption)"
"os_build=$($os.BuildNumber)"
"os_arch=$($os.OSArchitecture)"
$up = (Get-Date) - $os.LastBootUpTime
"uptime_days=$($up.Days)"
"boot=$($os.LastBootUpTime.ToString('yyyy-MM-ddTHH:mm:ss'))"
"mem_total_mb=$([math]::Round($os.TotalVisibleMemorySize/1024))"
"mem_free_mb=$([math]::Round($os.FreePhysicalMemory/1024))"
$drives = Get-PSDrive -PSProvider FileSystem | Where-Object { $_.Free -ne $null }
"disk=$((@($drives | ForEach-Object { "$($_.Name):free$([math]::Round($_.Free/1GB,1))G" }) -join ','))"
$ipv4 = Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -ne '127.0.0.1' -and $_.IPAddress -notlike '169.254.*' }
"ip=$((@($ipv4 | ForEach-Object { "$($_.IPAddress)/$($_.PrefixLength)" }) -join ','))"
$dns = Get-DnsClientServerAddress -AddressFamily IPv4 | Where-Object { $_.ServerAddresses }
"dns=$((@($dns | ForEach-Object { $_.ServerAddresses -join ',' }) -join ';'))"
$listen = Get-NetTCPConnection -State Listen | Select-Object -ExpandProperty LocalPort | Sort-Object -Unique
"listen_ports=$((@($listen) -join ','))"
$est = Get-NetTCPConnection -State Established
"estab_total=$(@($est).Count)"
$est | Group-Object RemotePort | Sort-Object Count -Descending | Select-Object -First 5 | ForEach-Object { "estab_port_$($_.Name)=$($_.Count)" }
$syserr = Get-WinEvent -FilterHashtable @{LogName='System';Level=2;StartTime=(Get-Date).AddHours(-24)} -MaxEvents 500
"err_system_24h=$(@($syserr).Count)"
$apperr = Get-WinEvent -FilterHashtable @{LogName='Application';Level=2;StartTime=(Get-Date).AddHours(-24)} -MaxEvents 500
"err_application_24h=$(@($apperr).Count)"
$mp = Get-MpComputerStatus
"defender_rtp=$($mp.RealTimeProtectionEnabled)"
"defender_av=$($mp.AntivirusEnabled)"
$fw = Get-NetFirewallProfile
"firewall=$((@($fw | ForEach-Object { "$($_.Name):$($_.Enabled)" }) -join ','))"
"reboot_pending=$(Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending')"
"wua_reboot=$(Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\WindowsUpdate\Auto Update\RebootRequired')"
$cs = Get-CimInstance Win32_ComputerSystem
"domain=$($cs.Domain)"
"model=$($cs.Manufacturer) $($cs.Model)"`

// quickLinux — то же для Linux-гостя (оффлайн-путь с загрузочной флешки).
const quickLinux = `export LC_ALL=C
echo "host=$(hostname)"
echo "user=$(id -un)"
echo "os=$(uname -sr)"
echo "boot=$(uptime -s 2>/dev/null)"
echo "uptime_days=$(( $(awk '{print int($1)}' /proc/uptime) / 86400 ))"
echo "mem_total_mb=$(awk '/MemTotal/{print int($2/1024)}' /proc/meminfo)"
echo "mem_free_mb=$(awk '/MemAvailable/{print int($2/1024)}' /proc/meminfo)"
echo "disk=$(df -P / | awk 'NR==2{printf "free%0.1fG", $4/1048576}')"
echo "ip=$(ip -4 -br addr 2>/dev/null | awk '$1!="lo" && $3!="" {printf "%s%s", sep, $3; sep=","}')"
echo "dns=$(awk '/^nameserver/{printf "%s%s", sep, $2; sep=","}' /etc/resolv.conf 2>/dev/null)"
echo "listen_ports=$(ss -Hltn 2>/dev/null | awk '{print $4}' | sed 's/.*://' | sort -un | paste -sd, -)"
echo "estab_total=$(ss -Htn state established 2>/dev/null | wc -l)"
echo "err_system_24h=$(journalctl -p 3 -S -24h --no-pager -q 2>/dev/null | wc -l)"
echo "reboot_pending=$(test -f /var/run/reboot-required && echo True || echo False)"
echo "firewall=$(if command -v ufw >/dev/null 2>&1; then ufw status 2>/dev/null | head -1; else echo unknown; fi)"`

// reQuickField — что считается фактом в выводе. Всё остальное (баннеры
// PowerShell, предупреждения локализации, «At line:1 char:1») отбрасывается:
// в снимок попадает только то, что имеет имя.
var reQuickField = regexp.MustCompile(`^([a-z][a-z0-9_]*)=(.*)$`)

// runQuickProbe — снять быстрый снимок. Возвращает готовый текст для модели и
// консоли; raw-вывод при этом не теряется: если полей не нашлось, он уходит в
// quick_raw, иначе «пусто» не отличить от «команда не запустилась».
func runQuickProbe() (string, int, error) {
	out, rc, err := runCommand(quickCommand(), quickTimeout)
	if err != nil {
		return "", rc, err
	}
	return compactQuick(out, rc), rc, nil
}

// compactQuick — оставить только key=value, нормализовать имя и обрезать
// длинные значения. Локаль вывода не важна: имена полей пишем мы сами.
func compactQuick(raw string, rc int) string {
	var b strings.Builder
	fields := 0
	fmt.Fprintf(&b, "quick=%s\n", time.Now().Format(time.RFC3339))
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimPrefix(line, "\ufeff")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#<") {
			continue
		}
		m := reQuickField.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		fmt.Fprintf(&b, "%s=%s\n", m[1], truncate(strings.TrimSpace(m[2]), quickValueMax))
		fields++
	}
	if rc != 0 {
		fmt.Fprintf(&b, "quick_exit=%d\n", rc)
	}
	if fields == 0 {
		one := strings.Join(strings.Fields(raw), " ")
		fmt.Fprintf(&b, "quick_error=ни одного поля\nquick_raw=%s\n", truncate(one, quickRawMax))
	}
	return b.String()
}

// printGuestReport — показать отчёт В КОНСОЛИ КЛИЕНТА. Это не лог и не файл:
// клиент смотрит на своё окно, и отчёт должен быть виден там, куда он смотрит.
// Рамка — чтобы текст не слился с прогрессом сессии.
func printGuestReport(text string) {
	const width = 64
	fmt.Println()
	fmt.Println(strings.Repeat("=", width))
	fmt.Println("  ОТЧЁТ О РАБОТЕ")
	fmt.Println(strings.Repeat("=", width))
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Println("  " + line)
	}
	fmt.Println(strings.Repeat("=", width))
	fmt.Println("  Это окно можно закрыть — проверка закончена.")
	fmt.Println()
}

// toolQuickProbe — быстрый снимок как декларативный инструмент: агент вызывает
// его сам, без текста команды и без риска «модель придумала не ту утилиту».
func toolQuickProbe(args map[string]any) (string, error) {
	_ = args
	out, _, err := runQuickProbe()
	if err != nil {
		return "", err
	}
	return out, nil
}
