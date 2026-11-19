package main

import (
	"fmt"
	"strings"
)

// Тормоз цикла «предложил → отказ → предложил снова».
//
// Повод — полевой прогон 19.11.2026 на GIGA (уровень L1, кейс «системный диск
// ноутбука HP»): за 14 минут агент выдал 124 реплики, из которых около шести
// были ответом на слова человека. `bcdboot C:\Windows /f UEFI` отклонялся
// пять раз, `bcdedit /enum all /v` — три. Механика: оболочка исполняет блок
// ```cmd из ответа, отказ возвращает модели новой репликой user, auto_followup
// запускает ещё один ответ — и так по кругу, без оператора.
//
// Здесь две границы:
//   - повтор команды, уже отклонённой политикой, не выполняется вовсе (иначе
//     журнал наполняется одинаковыми отказами, а модель считает, что «попробует
//     ещё раз»);
//   - серия отказов подряд останавливает цепочку, и слово возвращается человеку.
//
// Счётчики не сбрасываются на каждой реплике оператора: иначе «та же команда
// снова» проходит ровно потому, что человек написал ещё одну фразу. Сбрасывают
// их только смена уровня и /clear — там решение меняется по существу.
const (
	guardRepeatMax = 2 // первая вариация после отказа допустима, дальше — нет
	guardStreakMax = 3 // сколько отказов подряд до остановки цепочки
)

type commandGuard struct {
	refusals map[string]int // канонизированная команда -> сколько раз отказана политикой
	streak   int            // отказов подряд в текущем ответе модели
}

func newCommandGuard() *commandGuard {
	return &commandGuard{refusals: map[string]int{}}
}

// canonCommand сравнивает команды по смыслу, а не по регистру и пробелам:
// модель повторяет ту же команду в другом регистре и с лишними пробелами.
func canonCommand(cmd string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(cmd))), " ")
}

// beginTurn — новый ответ модели: серия отказов считается внутри ответа.
func (g *commandGuard) beginTurn() { g.streak = 0 }

// check вызывается ДО выполнения. blocked=true — это повтор отклонённой
// команды; note идёт в историю вместо обычного отчёта об исполнении.
func (g *commandGuard) check(cmd string) (blocked bool, note string) {
	n := g.refusals[canonCommand(cmd)]
	if n < guardRepeatMax {
		return false, ""
	}
	return true, fmt.Sprintf("уже отклонялась политикой %d раз(а); повтор не выполняется — "+
		"команда вне текущего уровня, решение за оператором (L3 или ручной запуск)", n)
}

// refused фиксирует отказ оболочки. stop=true — серия достигла предела,
// цепочку авто-продолжений пора остановить. Отказ самого оператора (он не
// подтвердил y/n) в счёт не идёт: это не политика, а выбор человека, и он
// вправе передумать на следующей реплике.
func (g *commandGuard) refused(cmd, note string) (stop bool, streak int) {
	if !isPolicyRefusal(note) {
		g.streak = 0
		return false, 0
	}
	g.refusals[canonCommand(cmd)]++
	g.streak++
	return g.streak >= guardStreakMax, g.streak
}

// ran — команда выполнена (пусть и с кодом 1): это прогресс, серия сброшена.
func (g *commandGuard) ran() { g.streak = 0 }

// reset — смена уровня или /clear: прежние отказы больше не приговор.
func (g *commandGuard) reset() {
	g.refusals = map[string]int{}
	g.streak = 0
}

// isPolicyRefusal отличает отказ политики (команда вне уровня) от отказа
// оператора. Формулировки — из handleCommand; меняются там же.
func isPolicyRefusal(note string) bool {
	return strings.HasPrefix(note, "запрещена политикой") ||
		strings.HasPrefix(note, "нет в белом списке")
}
