package main

import (
	"strings"
	"testing"
)

func TestConfiguredSecretRefs(t *testing.T) {
	cfg := Config{Providers: []Provider{
		{Name: "DeepSeek", APIKey: "sk-0123456789abcdef0123456789abcdef"},
		{Name: "Образец", APIKey: "sk-ВСТАВЬТЕ_КЛЮЧ"},
		{Name: "Ollama", APIKey: "ollama"},
		{Name: "Короткий", APIKey: "short"},
	}}
	refs := configuredSecretRefs(cfg)
	if len(refs) != 1 || refs[0].Label != "DeepSeek" {
		t.Fatalf("ожидался один ключ DeepSeek, получили %+v", refs)
	}
}

func TestScanSecretsDoesNotLeakValue(t *testing.T) {
	key := "sk-abcdef0123456789abcdef0123456789"
	refs := []secretRef{{Label: "DeepSeek", Value: key}}
	found := scanSecretsBinary("файл клиента", []byte("prefix "+key+" suffix"), refs)
	if len(found) != 1 || !strings.Contains(found[0], "DeepSeek") {
		t.Fatalf("ключ не найден: %v", found)
	}
	if strings.Contains(found[0], key) {
		t.Fatalf("значение секрета попало в отчёт: %s", found[0])
	}
	if got := scanSecretsBinary("файл клиента", []byte("нет ключа"), refs); len(got) != 0 {
		t.Fatalf("ложное срабатывание: %v", got)
	}
}

func TestScanSecretsTextPatterns(t *testing.T) {
	clean := []byte("обычный текст сессии, ошибок нет")
	if got := scanSecretsText("архив", clean, nil); len(got) != 0 {
		t.Fatalf("ложное срабатывание на чистом тексте: %v", got)
	}
	found := scanSecretsText("архив", []byte("Authorization: Bearer sk-0123456789abcdef0123456789"), nil)
	if len(found) != 1 {
		t.Fatalf("шаблон sk- не пойман: %v", found)
	}
	if strings.Contains(found[0], "0123456789abcdef") {
		t.Fatalf("в отчёт попал хвост ключа: %s", found[0])
	}
}

func TestScanSecretsTextFindsConfiguredKey(t *testing.T) {
	key := "ghp_0123456789abcdefghijABCDEF"
	refs := []secretRef{{Label: "GitHub", Value: key}}
	found := scanSecretsText("уроки", []byte("строка: "+key), refs)
	if len(found) == 0 || !strings.Contains(found[0], "GitHub") {
		t.Fatalf("точный ключ не найден: %v", found)
	}
}
