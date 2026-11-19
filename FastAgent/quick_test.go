package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// compactQuick — единственное место, где сырой вывод превращается в снимок.
// Проверяем две вещи: факты не теряются и шум PowerShell в снимок не попадает.
func TestCompactQuickKeepsOnlyFields(t *testing.T) {
	raw := "\ufeff#< CLIXML\nhost=PC1\n\nAt line:1 char:1\nmem_free_mb=123\n" +
		"listen_ports=135,445\n  dns=192.168.0.1,8.8.8.8\n"
	got := compactQuick(raw, 0)
	for _, want := range []string{"host=PC1", "mem_free_mb=123", "listen_ports=135,445",
		"dns=192.168.0.1,8.8.8.8", "quick="} {
		if !strings.Contains(got, want) {
			t.Errorf("в снимке нет %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"CLIXML", "At line"} {
		if strings.Contains(got, bad) {
			t.Errorf("шум %q попал в снимок:\n%s", bad, got)
		}
	}
}

// Пустой снимок не имеет права выглядеть как «всё чисто»: без полей он
// называется ошибкой и оставляет сырой вывод — иначе «команда не запустилась»
// не отличить от «проверять нечего».
func TestCompactQuickEmptyIsHonest(t *testing.T) {
	got := compactQuick("какая-то ошибка PowerShell\n", 1)
	if !strings.Contains(got, "quick_error=") {
		t.Fatalf("нет пометки об ошибке:\n%s", got)
	}
	if !strings.Contains(got, "quick_exit=1") {
		t.Fatalf("нет кода возврата:\n%s", got)
	}
	if !strings.Contains(got, "какая-то ошибка") {
		t.Fatalf("сырой вывод потерян:\n%s", got)
	}
}

// Оба сценария быстрых проверок должны давать один и тот же набор ключей: это
// контракт для модели и для сценариев, а не «как получилось на конкретной ОС».
func TestQuickScriptsCoverBaseKeys(t *testing.T) {
	keys := []string{"host=", "os=", "mem_free_mb=", "listen_ports=",
		"estab_total=", "err_system_24h=", "firewall=", "reboot_pending="}
	for name, script := range map[string]string{"windows": quickWindows, "linux": quickLinux} {
		for _, k := range keys {
			if !strings.Contains(script, k) {
				t.Errorf("%s: в снимке нет ключа %q", name, k)
			}
		}
	}
}

// Консоль: quick и report — это команды оператора, и разбор не должен
// превращать текст отчёта в список слов.
func TestParseRemoteCommandQuickAndReport(t *testing.T) {
	kind, _, quit, err := parseRemoteCommand("quick")
	if err != nil || quit || kind != "quick" {
		t.Fatalf("quick: kind=%q quit=%v err=%v", kind, quit, err)
	}

	for _, line := range []string{"report Компьютер проверен, всё хорошо", "say Компьютер проверен, всё хорошо"} {
		kind, args, quit, err := parseRemoteCommand(line)
		if err != nil || quit || kind != "report" {
			t.Fatalf("%q: kind=%q quit=%v err=%v", line, kind, quit, err)
		}
		var a struct {
			Text string `json:"text"`
		}
		if jerr := json.Unmarshal(args, &a); jerr != nil {
			t.Fatalf("%q: %v", line, jerr)
		}
		if a.Text != "Компьютер проверен, всё хорошо" {
			t.Fatalf("%q: текст отчёта искажён: %q", line, a.Text)
		}
	}

	// Пустой report — не ошибка разбора: текст соберётся из архива сессии.
	if kind, args, _, err := parseRemoteCommand("report"); err != nil || kind != "report" || len(args) == 0 {
		t.Fatalf("пустой report: kind=%q args=%s err=%v", kind, args, err)
	}
}

// Отчёт для клиента: цифры из архива, без путей и жаргона.
func TestClientReportFromArchive(t *testing.T) {
	a := newSessionArchive("abc123", "http://127.0.0.1:8866")
	a.GuestHost = "DESKTOP-3DHVKKC"
	a.Add("quick", "quick", remoteResult{OK: true, Exit: 0, Out: "host=DESKTOP-3DHVKKC"})
	a.Add("read C:\\нет.txt", "read_file", remoteResult{OK: false, Exit: 1, Out: "файл не найден"})
	got := a.ClientReport()
	for _, want := range []string{"DESKTOP-3DHVKKC", "Выполнено шагов: 2", "Отказов защиты: 1",
		"Если проблема повторится"} {
		if !strings.Contains(got, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, got)
		}
	}
}

// printGuestReport печатает в СВОЙ stdout — значит, в окно клиента. Здесь
// проверяем, что текст действительно туда попадает и обёрнут рамкой.
func TestPrintGuestReportFramesText(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printGuestReport("Компьютер: PC1\nИтог: проверено")
	w.Close()
	os.Stdout = old

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"ОТЧЁТ О РАБОТЕ", "Компьютер: PC1", "Итог: проверено",
		"Окно не закрывайте"} {
		if !strings.Contains(got, want) {
			t.Errorf("в консоли клиента нет %q:\n%s", want, got)
		}
	}
}
