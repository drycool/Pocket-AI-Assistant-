package main

// Тесты политики доступа: главный «замок» FastAgent. Проверяют, что команда
// получает правильный вердикт на каждом уровне L0..L3 — без сети и без запуска.

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		cmd    string
		level  int
		expect Verdict
	}{
		// Чтение: доступно с L1, без подтверждения.
		{"ipconfig /all", L0Chat, VForbidden},
		{"ipconfig /all", L1Read, VReadOnly},
		{"systeminfo", L1Read, VReadOnly},
		{"Get-EventLog -LogName System -Newest 5", L1Read, VReadOnly},
		{"Test-NetConnection google.com", L1Read, VReadOnly},
		{"reg query HKLM\\Software", L1Read, VReadOnly},
		{"uname -a", L1Read, VReadOnly},
		{"whoami", L1Read, VReadOnly},

		// Починки: только с L2 и с подтверждением.
		{"ipconfig /flushdns", L1Read, VNotAllowed},
		{"ipconfig /flushdns", L2Fix, VNeedsYes},
		{"ipconfig /release", L1Read, VNotAllowed},
		{"ipconfig /release", L2Fix, VNeedsYes},
		{"ipconfig /all", L1Read, VReadOnly},
		{"netsh int ip reset", L2Fix, VNeedsYes},
		{"netsh wlan show profiles", L1Read, VReadOnly},
		{"netsh winsock reset", L2Fix, VNeedsYes},
		{"Restart-Service Spooler", L2Fix, VNeedsYes},
		{"sfc /scannow", L2Fix, VNeedsYes},
		{"sfc /verifyonly", L1Read, VReadOnly},
		{"systemctl restart nginx", L2Fix, VNeedsYes},
		{"systemctl status nginx", L1Read, VReadOnly},

		// Незнакомая команда: запрещена до L3, на L3 — с подтверждением.
		{"mycustomtool --scan", L1Read, VNotAllowed},
		{"mycustomtool --scan", L2Fix, VNotAllowed},
		{"mycustomtool --scan", L3Admin, VNeedsYes},

		// Опасное: только L3 и с двойным подтверждением.
		{"format C: /fs:ntfs", L2Fix, VForbidden},
		{"format C: /fs:ntfs", L3Admin, VDangerous},
		{"diskpart", L3Admin, VDangerous},
		{"bcdedit /set safeboot minimal", L3Admin, VDangerous},
		{"reg delete HKLM\\Software\\Foo /f", L3Admin, VDangerous},
		{"del /f /s /q C:\\temp", L3Admin, VDangerous},
		{"Remove-Item C:\\data -Recurse -Force", L3Admin, VDangerous},
		{"shutdown /r /t 0", L3Admin, VDangerous},
		{"rm -rf /", L3Admin, VDangerous},
		{"mkfs.ext4 /dev/sda1", L3Admin, VDangerous},
		{"Set-ExecutionPolicy Bypass -Scope Process", L3Admin, VDangerous},
		{"net user admin NewPass123", L3Admin, VDangerous},
		{"del /f /s /q C:\\temp", L1Read, VForbidden},

		// Пустая строка — запрещено на любом уровне.
		{"   ", L3Admin, VForbidden},
		{"cmd /c ipconfig /flushdns", L2Fix, VNeedsYes},
	}

	for _, c := range cases {
		got := classify(c.cmd, c.level)
		if got != c.expect {
			t.Errorf("classify(%q, L%d) = %v, ожидалось %v", c.cmd, c.level, got, c.expect)
		}
	}
}

func TestExtractCommands(t *testing.T) {
	answer := "Проверю сеть:\n\n```cmd\nipconfig /all\nipconfig /flushdns\n```\n\n" +
		"И ещё модуль:\n```powershell\nGet-Service Spooler | Select-Object Status\n```\n" +
		"а также текст без блока\n"
	got := extractCommands(answer)
	want := []string{"ipconfig /all", "ipconfig /flushdns", "Get-Service Spooler | Select-Object Status"}
	if len(got) != len(want) {
		t.Fatalf("получено %d команд %v, ожидалось %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("команда %d: получено %q, ожидалось %q", i, got[i], want[i])
		}
	}
}

func TestBuildMessagesLimitsHistory(t *testing.T) {
	cfg := defaultConfig()
	cfg.HistoryLimit = 4
	var history []Message
	for i := 0; i < 20; i++ {
		history = append(history, Message{Role: "user", Content: "msg"})
	}
	msgs := buildMessages(cfg, history)
	// 1 системное сообщение + не больше HistoryLimit из истории
	if len(msgs) != cfg.HistoryLimit+1 {
		t.Fatalf("ожидалось %d сообщений, получено %d", cfg.HistoryLimit+1, len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("первым должно идти system, получено %q", msgs[0].Role)
	}
}

func TestEndpointNormalization(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/v1":                  "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/v1/":                 "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/v1/chat/completions": "https://api.deepseek.com/v1/chat/completions",
		"http://192.168.11.111:11434/v1":               "http://192.168.11.111:11434/v1/chat/completions",
	}
	for in, want := range cases {
		if got := endpoint(Provider{BaseURL: in}); got != want {
			t.Errorf("endpoint(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}
