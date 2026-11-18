// secrets.go — R5: контроль секретов и эталона файла.
//
// Оператор отдаёт клиенту исполняемый файл и держит у себя ключ модели. Два
// обязательных вопроса, которые нельзя пропустить в живой сессии:
//   1) тот ли это файл — размер и sha256 против эталона (manifest.json);
//   2) не утёк ли ключ — в файл клиента, в архив или в журнал гостя.
//
// Правило: значение секрета не печатается никогда. В отчёте — только имя
// провайдера и «найдено», иначе контроль сам становится утечкой в терминал,
// логи и скриншоты AnyDesk.

package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// secretPatterns — сильные признаки настоящего ключа. Слабые формы
// («token=», «password:») сюда не входят: ложное «нашли секрет» на чужой
// машине дороже пропуска, а настоящие ключи первых провайдеров ловятся ниже
// точным значением из конфига.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{30,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// secretRef — ключ из конфига: имя провайдера и само значение (только в памяти).
type secretRef struct {
	Label string
	Value string
}

// configuredSecretRefs — что именно мы обязаны не отдать клиенту: ключи
// провайдеров из рабочего конфига. Плейсхолдеры образца игнорируются.
func configuredSecretRefs(cfg Config) []secretRef {
	var refs []secretRef
	for i, p := range cfg.Providers {
		v := strings.TrimSpace(p.APIKey)
		if len(v) < 12 {
			continue
		}
		if strings.Contains(strings.ToUpper(v), "ВСТАВЬТЕ") || strings.EqualFold(v, "ollama") {
			continue
		}
		label := strings.TrimSpace(p.Name)
		if label == "" {
			label = fmt.Sprintf("провайдер %d", i+1)
		}
		refs = append(refs, secretRef{Label: label, Value: v})
	}
	return refs
}

// scanSecretsBinary — в бинарнике ищем только точные значения ключей.
// Общие шаблоны по 6 МиБ машинного кода дают ложные срабатывания.
func scanSecretsBinary(label string, data []byte, refs []secretRef) []string {
	var found []string
	for _, r := range refs {
		if bytes.Contains(data, []byte(r.Value)) {
			found = append(found, fmt.Sprintf("%s: найден ключ «%s»", label, r.Label))
		}
	}
	return found
}

// scanSecretsText — в тексте (архив, уроки, журнал гостя) ищем и точные
// значения, и общие формы ключей.
func scanSecretsText(label string, data []byte, refs []secretRef) []string {
	found := scanSecretsBinary(label, data, refs)
	for _, re := range secretPatterns {
		if m := re.Find(data); m != nil {
			found = append(found, fmt.Sprintf("%s: строка похожа на секрет (%s…)", label, safePrefix(m)))
		}
	}
	return found
}

// safePrefix — первые символы находки, чтобы оператор понял, о чём речь,
// но не получил ключ целиком в терминал и скриншот.
func safePrefix(b []byte) string {
	const n = 8
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

// verifyTicket — сверка файла, который уходит клиенту, с эталоном.
// Проверяются оба измерения: размер (быстро, ловит обрезанную копию) и sha256
// (точно, ловит подмену). Возвращает человеческую строку для экрана.
func verifyTicket(path string) (ok bool, detail string) {
	m, err := loadManifest()
	if err != nil {
		return false, "эталон недоступен: " + err.Error()
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false, "файл не найден: " + err.Error()
	}
	if fi.Size() != m.GuestExe.Size {
		return false, fmt.Sprintf("размер %d Б, а эталон %d Б", fi.Size(), m.GuestExe.Size)
	}
	sum, err := remoteFileSum(path)
	if err != nil {
		return false, "sha256 не посчитан: " + err.Error()
	}
	if !strings.EqualFold(sum, m.GuestExe.SHA256) {
		return false, fmt.Sprintf("sha256 %s…, а эталон %s…", safePrefix([]byte(sum)), safePrefix([]byte(m.GuestExe.SHA256)))
	}
	return true, fmt.Sprintf("%s, %s, sha256 %s… — совпадает", filepathBase(path), humanBytes(fi.Size()), safePrefix([]byte(sum)))
}

// filepathBase — имя файла без пути (не тянем filepath ради одной строки в тестах).
func filepathBase(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// humanBytes — размер по-человечески: 6.4 МиБ, 812 КиБ.
func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d Б", n)
	case n < k*k:
		return fmt.Sprintf("%.1f КиБ", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f МиБ", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.1f ГиБ", float64(n)/(k*k*k))
}
