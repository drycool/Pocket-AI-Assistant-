package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

// pipeWriter отдаёт текст порциями и не блокирует тест, если читатель
// остановится раньше: каждая запись идёт в отдельной горутине.
func pipeWriter(pw *io.PipeWriter, chunks ...string) {
	go func() {
		for _, c := range chunks {
			_, _ = pw.Write([]byte(c))
		}
	}()
}

// Вставка многострочного вывода — ОДНА реплика. До правки (19.11.2026, GIGA)
// каждая строка вставленного `dir` становилась отдельным ходом к модели.
func TestReadTurnСклеиваетВставку(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	s := newLineSource(pr)
	pipeWriter(pw, "10.01.2018  21:25    <DIR>          .\n",
		"11.01.2018  00:13            42 861 cbs.log\n",
		"C:\\Windows\\system32>\n")

	got, err := s.ReadTurn(400 * time.Millisecond)
	if err != nil {
		t.Fatalf("ReadTurn: %v", err)
	}
	want := "10.01.2018  21:25    <DIR>          .\n" +
		"11.01.2018  00:13            42 861 cbs.log\n" +
		"C:\\Windows\\system32>"
	if got != want {
		t.Fatalf("склейка не сработала:\n got=%q\nwant=%q", got, want)
	}
}

// Одна строка и тишина после неё — реплика ровно из неё, без пустого хвоста.
func TestReadTurnОднаСтрока(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	s := newLineSource(pr)
	pipeWriter(pw, "systeminfo\n")

	got, err := s.ReadTurn(150 * time.Millisecond)
	if err != nil {
		t.Fatalf("ReadTurn: %v", err)
	}
	if got != "systeminfo" {
		t.Fatalf("одна строка испорчена: %q", got)
	}
}

// Конец ввода: отданное не теряется, следующая реплика сообщает io.EOF.
func TestReadTurnКонецВвода(t *testing.T) {
	pr, pw := io.Pipe()
	s := newLineSource(pr)
	go func() {
		_, _ = pw.Write([]byte("bcdedit /enum all\n"))
		_ = pw.Close()
	}()

	got, err := s.ReadTurn(150 * time.Millisecond)
	if err != nil {
		t.Fatalf("первая реплика: %v", err)
	}
	if got != "bcdedit /enum all" {
		t.Fatalf("got %q", got)
	}
	if _, err := s.ReadTurn(50 * time.Millisecond); err != io.EOF {
		t.Fatalf("ждали io.EOF, получили %v", err)
	}
}

// Пачка больше лимита режется: в одну реплику не уезжает тысяча строк.
func TestReadTurnРежетДлиннуюВставку(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("строка\n")
	}
	pr, pw := io.Pipe()
	defer pr.Close()
	s := newLineSource(pr)
	pipeWriter(pw, b.String())

	first, err := s.ReadTurn(400 * time.Millisecond)
	if err != nil {
		t.Fatalf("первая реплика: %v", err)
	}
	if n := strings.Count(first, "\n") + 1; n != turnMaxLines {
		t.Fatalf("в реплике %d строк, ждали предел %d", n, turnMaxLines)
	}
	rest, err := s.ReadTurn(400 * time.Millisecond)
	if err != nil {
		t.Fatalf("остаток: %v", err)
	}
	if n := strings.Count(rest, "\n") + 1; n != 300-turnMaxLines {
		t.Fatalf("в остатке %d строк, ждали %d", n, 300-turnMaxLines)
	}
}

// Подтверждение берёт РОВНО одну строку: «yes» из очереди не склеивается с
// соседними строками и не закрывает вопрос, которого оператор не видел.
func TestReadStringПоОдной(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	s := newLineSource(pr)
	pipeWriter(pw, "yes\n", "/clear\n")

	if !confirm(s, "", "yes") {
		t.Fatal("подтверждение не сработало")
	}
	next, err := s.ReadString('\n')
	if err != nil {
		t.Fatalf("вторая строка: %v", err)
	}
	if strings.TrimSpace(next) != "/clear" {
		t.Fatalf("вторая строка испорчена: %q", next)
	}
}
