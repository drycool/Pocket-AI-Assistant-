package main

import (
	"bufio"
	"io"
	"strings"
	"time"
)

// lineReader — всё, что диалогу нужно от источника строк: реплика оператора и
// подтверждение «y/n» читаются одинаково. Реализации: *bufio.Reader (обычное
// чтение) и *lineSource (очередь stdin со склейкой пачки).
type lineReader interface {
	ReadString(delim byte) (string, error)
}

// Границы одной реплики-вставки. Полный `bcdedit /enum all` или `dir /a` дают
// сотню строк — целиком модели это не нужно, а лишние килобайты в истории
// стоят токенов на каждой следующей реплике.
const (
	turnQuiet    = 250 * time.Millisecond // тишина после строки = конец пачки
	turnMaxLines = 200
	turnMaxBytes = 12000
)

// lineSource — единственный читатель stdin на сеанс.
//
// Зачем не читать *bufio.Reader напрямую: вставка многострочного вывода в
// консоль отдаётся по строке на каждое чтение, и цикл чата вызывал модель на
// каждую строку. На прогоне 19.11.2026 (GIGA, 17:08–17:22) одна вставка `dir`
// дала 40 одинаковых ответов «Это не реплика «Вывод команды … (код N)» —
// команда не выполнялась», а оператор остановил сеанс. Очередь и склейка пачки
// делают из вставки одну реплику; подтверждения по-прежнему берут строки по
// одной, иначе «yes» из вставки закрыло бы вопрос, которого оператор не видел.
type lineSource struct {
	lines chan string
}

func newLineSource(r io.Reader) *lineSource {
	s := &lineSource{lines: make(chan string, 256)}
	go func() {
		br := bufio.NewReader(r)
		defer close(s.lines)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				s.lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

// ReadString отдаёт ровно одну строку — для подтверждений «y/n» и «yes».
func (s *lineSource) ReadString(delim byte) (string, error) {
	line, ok := <-s.lines
	if !ok {
		return "", io.EOF
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	return line, nil
}

// ReadTurn собирает реплику оператора: первая строка плюс всё, что пришло
// следом без паузы длиннее quiet. Строки склеиваются через "\n".
func (s *lineSource) ReadTurn(quiet time.Duration) (string, error) {
	first, err := s.ReadString('\n')
	if err != nil {
		return "", err
	}
	parts := []string{strings.TrimRight(first, "\r\n")}
	size := len(first)
	for len(parts) < turnMaxLines && size < turnMaxBytes {
		timer := time.NewTimer(quiet)
		select {
		case line, ok := <-s.lines:
			timer.Stop()
			if !ok {
				return strings.TrimSpace(strings.Join(parts, "\n")), nil
			}
			parts = append(parts, strings.TrimRight(line, "\r\n"))
			size += len(line)
		case <-timer.C:
			return strings.TrimSpace(strings.Join(parts, "\n")), nil
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}
