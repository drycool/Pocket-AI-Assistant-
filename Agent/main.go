package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type GeminiRequest struct {
	Contents          []Content          `json:"contents"`
	SystemInstruction *SystemInstruction `json:"system_instruction,omitempty"`
}

type SystemInstruction struct {
	Parts []Part `json:"parts"`
}

type Content struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

type Part struct {
	Text string `json:"text"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []Part `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type SessionData struct {
	SessionID string    `json:"session_id"`
	UpdatedAt string    `json:"updated_at"`
	History   []Content `json:"history"`
}

type Config struct {
	GeminiAPIKey string `json:"GEMINI_API_KEY"`
}

var (
	baseDir     string
	dataDir     string
	sessionsDir string
	configFile  string
	reportsDir  string
	logsDir     string
)

const SystemPrompt = "Ты — карманный AI-инженер и диагност систем Windows/Linux. " +
	"Твоя задача — анализировать проблемы гостевого ПК, давать точные PowerShell, CMD или Bash команды " +
	"и помогать локализовать сбои. Отвечай лаконично и по делу."

func initPaths() {
	execPath, err := os.Executable()
	if err != nil {
		execPath, _ = filepath.Abs(".")
	}
	baseDir = filepath.Dir(filepath.Dir(execPath))
	dataDir = filepath.Join(baseDir, "Data")
	sessionsDir = filepath.Join(dataDir, "sessions")
	reportsDir = filepath.Join(dataDir, "reports")
	logsDir = filepath.Join(dataDir, "logs")
	configFile = filepath.Join(dataDir, "config.json")

	_ = os.MkdirAll(sessionsDir, 0755)
	_ = os.MkdirAll(reportsDir, 0755)
	_ = os.MkdirAll(logsDir, 0755)
}

func getAPIKey(reader *bufio.Reader) string {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey != "" {
		return apiKey
	}

	if data, err := os.ReadFile(configFile); err == nil {
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err == nil && strings.TrimSpace(cfg.GeminiAPIKey) != "" {
			return strings.TrimSpace(cfg.GeminiAPIKey)
		}
	}

	for {
		fmt.Println("\n[!] GEMINI_API_KEY не найден.")
		fmt.Print("Введите ваш Gemini API Key: ")
		apiKey, _ = reader.ReadString('\n')
		apiKey = strings.TrimSpace(apiKey)

		if apiKey != "" {
			break
		}
		fmt.Println("[ERROR] Ключ не может быть пустым!")
	}

	fmt.Print("Сохранить ключ в Data/config.json на флешке? (y/n) [y]: ")
	saveChoice, _ := reader.ReadString('\n')
	saveChoice = strings.TrimSpace(strings.ToLower(saveChoice))

	if saveChoice == "" || saveChoice == "y" {
		cfg := Config{GeminiAPIKey: apiKey}
		if cfgData, err := json.MarshalIndent(cfg, "", "  "); err == nil {
			_ = os.WriteFile(configFile, cfgData, 0644)
			fmt.Println("[OK] Ключ сохранен в Data/config.json")
		}
	}

	return apiKey
}

func atomicSaveSession(sessionFile string, sessionID string, history []Content) {
	data := SessionData{
		SessionID: sessionID,
		UpdatedAt: time.Now().Format(time.RFC3339),
		History:   history,
	}
	fileData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}

	tmpFile := sessionFile + ".tmp"
	if err := os.WriteFile(tmpFile, fileData, 0644); err == nil {
		_ = os.Rename(tmpFile, sessionFile)
	}
}

// Быстрый сбор системных данных через PowerShell
func gatherDiagnostics(sessionID string) (string, string) {
	fmt.Print("\n[*] Запуск экспресс-диагностики системы...")

	psScript := `
$ErrorActionPreference = 'SilentlyContinue'
$os  = Get-CimInstance Win32_OperatingSystem
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
$ramFreeGB  = [math]::Round($os.FreePhysicalMemory / 1MB, 2)
$ramTotalGB = [math]::Round($os.TotalVisibleMemorySize / 1MB, 2)
$disks = @(Get-CimInstance Win32_LogicalDisk | Where-Object DriveType -eq 3 | ForEach-Object { "$($_.DeviceID) Free:$([math]::Round($_.FreeSpace/1GB,1))GB/$([math]::Round($_.Size/1GB,1))GB" })
$net = @(Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.InterfaceAlias -notlike '*Loopback*' } | Select-Object -First 3)
$events = @(Get-WinEvent -FilterHashtable @{LogName='System'; Level=1,2} -MaxEvents 5 | ForEach-Object { "[$($_.TimeCreated.ToString('HH:mm'))][$($_.ProviderName)] $($_.Message -replace [Environment]::NewLine, ' ')" })
Write-Output '=== SYSTEM DIAGNOSTIC DUMP ==='
Write-Output ("OS: " + $os.Caption + " (" + $os.Version + ")")
Write-Output ("CPU: " + $cpu.Name)
Write-Output ("RAM: " + $ramFreeGB + " GB free of " + $ramTotalGB + " GB")
if ($disks.Count -gt 0) { Write-Output ("Disks: " + ($disks -join ' | ')) }
Write-Output 'Network IPs:'
foreach ($n in $net) { Write-Output ("  - " + $n.InterfaceAlias + ": " + $n.IPAddress) }
Write-Output ''
Write-Output '=== RECENT CRITICAL EVENTS (System Log) ==='
if ($events.Count -gt 0) { foreach ($e in $events) { Write-Output $e } } else { Write-Output 'No critical errors found.' }
`

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	out, err := cmd.Output()
	rawOutput := ""
	if err != nil {
		rawOutput = fmt.Sprintf("Ошибка выполнения PowerShell диагностики: %v", err)
	} else {
		rawOutput = string(out)
	}

	// Сохранение полного дампа в Data/logs/
	logPath := filepath.Join(logsDir, fmt.Sprintf("sysinfo_%s.txt", sessionID))
	_ = os.WriteFile(logPath, []byte(rawOutput), 0644)

	fmt.Printf("\r[OK] Диагностика завершена. Лог сохранен в Data/logs/sysinfo_%s.txt\n", sessionID)
	return rawOutput, logPath
}

func generateReport(apiKey string, sessionID string, history []Content) {
	fmt.Println("\n[*] Генерация итогового отчета по сессии...")

	reportPrompt := "На основе всей истории нашего диалога выше, сформируй структурированный итоговый технический отчет " +
		"в формате Markdown для инженера/клиента.\n\n" +
		"Используй следующую структуру:\n" +
		"# Отчет о диагностике и ремонте ПК\n" +
		"**Сессия:** " + sessionID + "\n" +
		"**Дата:** " + time.Now().Format("2006-01-02 15:04") + "\n\n" +
		"## 1. Первичные симптомы и обращение\n" +
		"## 2. Результаты автодиагностики и выявленные проблемы\n" +
		"## 3. Выполненные действия и команды\n" +
		"## 4. Итоговый статус и рекомендации"

	reportReqHistory := append(history, Content{
		Role:  "user",
		Parts: []Part{{Text: reportPrompt}},
	})

	respText, err := sendGeminiRequest(apiKey, reportReqHistory)
	if err != nil {
		fmt.Printf("[ERROR] Ошибка при генерации отчета: %v\n", err)
		return
	}

	reportPath := filepath.Join(reportsDir, fmt.Sprintf("REPORT_%s.md", sessionID))
	err = os.WriteFile(reportPath, []byte(respText), 0644)
	if err != nil {
		fmt.Printf("[ERROR] Не удалось сохранить файл отчета: %v\n", err)
		return
	}

	fmt.Printf("[SUCCESS] Итоговый отчет успешно сохранен:\n -> %s\n\n", reportPath)
}

func selectOrCreateSession(reader *bufio.Reader) (string, string, []Content) {
	fmt.Println("\n=== POCKET AGENT (GO NATIVE) — МЕНЕДЖЕР СЕССИЙ ===")
	fmt.Println("1. Создать новую сессию диагностики")
	fmt.Println("2. Продолжить существующую сессию")
	fmt.Print("Выберите действие (1/2) [1]: ")

	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)

	if choice == "2" {
		entries, err := os.ReadDir(sessionsDir)
		if err == nil {
			var files []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".tmp") {
					files = append(files, entry.Name())
				}
			}

			if len(files) > 0 {
				sort.Strings(files)
				fmt.Println("\nДоступные сессии на флешке:")
				for idx, f := range files {
					fmt.Printf(" [%d] %s\n", idx+1, f)
				}
				fmt.Print("\nВыберите номер сессии: ")
				selStr, _ := reader.ReadString('\n')
				selIdx, err := strconv.Atoi(strings.TrimSpace(selStr))
				if err == nil && selIdx >= 1 && selIdx <= len(files) {
					sessionFile := filepath.Join(sessionsDir, files[selIdx-1])
					data, err := os.ReadFile(sessionFile)
					if err == nil {
						var sess SessionData
						if err := json.Unmarshal(data, &sess); err == nil {
							fmt.Printf("\n[OK] Сессия '%s' загружена (%d сообщений в истории)\n", sess.SessionID, len(sess.History))
							return sessionFile, sess.SessionID, sess.History
						}
					}
				}
			}
		}
		fmt.Println("[!] Сессии не найдены или выбор неверен. Создаем новую.")
	}

	fmt.Print("Идентификатор ПК (например, Work-PC) [Guest-PC]: ")
	pcName, _ := reader.ReadString('\n')
	pcName = strings.TrimSpace(pcName)
	if pcName == "" {
		pcName = "Guest-PC"
	}

	sessionID := fmt.Sprintf("%s_%s", time.Now().Format("2006-01-02_15-04"), pcName)
	sessionFile := filepath.Join(sessionsDir, sessionID+".json")

	var history []Content

	fmt.Print("Выполнить автодиагностику гостевого ПК прямо сейчас? (y/n) [y]: ")
	diagChoice, _ := reader.ReadString('\n')
	diagChoice = strings.TrimSpace(strings.ToLower(diagChoice))

	if diagChoice == "" || diagChoice == "y" {
		diagData, _ := gatherDiagnostics(sessionID)
		initMsg := fmt.Sprintf("Вот первичные данные экспресс-диагностики этого компьютера:\n\n```text\n%s\n```\nПроанализируй их и жди моих указаний.", diagData)
		history = append(history, Content{Role: "user", Parts: []Part{{Text: initMsg}}})
		history = append(history, Content{Role: "model", Parts: []Part{{Text: "Данные автодиагностики приняты и изучены. Готов к поиску проблем и выполнению команд."}}})
	}

	atomicSaveSession(sessionFile, sessionID, history)
	return sessionFile, sessionID, history
}

func sendGeminiRequest(apiKey string, history []Content) (string, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=%s", apiKey)

	reqBody := GeminiRequest{
		Contents: history,
		SystemInstruction: &SystemInstruction{
			Parts: []Part{{Text: SystemPrompt}},
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var geminiResp GeminiResponse
	if err := json.Unmarshal(bodyBytes, &geminiResp); err != nil {
		return "", err
	}

	if geminiResp.Error != nil {
		return "", fmt.Errorf("API Error: %s", geminiResp.Error.Message)
	}

	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		return geminiResp.Candidates[0].Content.Parts[0].Text, nil
	}

	return "Пустой ответ от модели.", nil
}

func main() {
	initPaths()
	reader := bufio.NewReader(os.Stdin)

	apiKey := getAPIKey(reader)
	sessionFile, sessionID, history := selectOrCreateSession(reader)

	fmt.Printf("\n--- АГЕНТ АКТИВЕН (Сессия: %s) ---\n", sessionID)
	fmt.Println("Команды: /report — создать отчет | /diag — повторить сбор | exit — выход\n")

	for {
		fmt.Print("Вы > ")
		userInput, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		userInput = strings.TrimSpace(userInput)
		if userInput == "" {
			continue
		}

		lower := strings.ToLower(userInput)
		if lower == "exit" || lower == "quit" {
			fmt.Println("\n[OK] Сессия сохранена. Завершение работы.")
			break
		}

		if lower == "/report" {
			generateReport(apiKey, sessionID, history)
			continue
		}

		if lower == "/diag" {
			diagData, _ := gatherDiagnostics(sessionID)
			updateMsg := fmt.Sprintf("Результаты повторной диагностики ПК:\n\n```text\n%s\n```", diagData)
			history = append(history, Content{Role: "user", Parts: []Part{{Text: updateMsg}}})

			fmt.Print("\nGemini > Анализирую обновленный дамп...")
			respText, err := sendGeminiRequest(apiKey, history)
			fmt.Print("\rGemini > ")
			if err == nil {
				fmt.Printf("%s\n\n", respText)
				history = append(history, Content{Role: "model", Parts: []Part{{Text: respText}}})
				atomicSaveSession(sessionFile, sessionID, history)
			}
			continue
		}

		history = append(history, Content{Role: "user", Parts: []Part{{Text: userInput}}})

		fmt.Print("\nGemini > Думаю...")
		respText, err := sendGeminiRequest(apiKey, history)
		fmt.Print("\rGemini > ")

		if err != nil {
			fmt.Printf("[ОШИБКА]: %v\n\n", err)
			history = history[:len(history)-1]
			continue
		}

		fmt.Printf("%s\n\n", respText)
		history = append(history, Content{Role: "model", Parts: []Part{{Text: respText}}})

		atomicSaveSession(sessionFile, sessionID, history)
	}
}
