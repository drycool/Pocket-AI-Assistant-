package main

import (
	"strings"
	"testing"
)

// Блок ```cmd-manual — инструкция человеку, а не задание оболочке. Живой случай
// 19.11.2026: команда, которую модель сама называла «оператору вручную», всё
// равно уходила на исполнение и возвращалась отказом — 33 отказа за сессию.
func TestExtractCommandsManualBlock(t *testing.T) {
	answer := "Тебе вручную, от администратора:\n\n```cmd-manual\nbcdboot C:\\Windows /f UEFI\n```\n\n" +
		"А я проверю разметку:\n```cmd\nGet-Partition | Select-Object DiskNumber,Type\n```\n"
	got := extractCommands(answer)
	if len(got) != 1 {
		t.Fatalf("ожидалась одна команда, получено %d: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "Get-Partition") {
		t.Errorf("извлечена не та команда: %q", got[0])
	}

	// Синонимы и регистр не важны; блок исполнения при этом работает.
	for _, body := range []string{
		"```manual\nformat C:\n```",
		"```for-operator\nshutdown /r\n```",
		"```HUMAN\ndiskpart\n```",
	} {
		if cmds := extractCommands(body); len(cmds) != 0 {
			t.Fatalf("блок %q исполнен: %v", body, cmds)
		}
	}
	if cmds := extractCommands("```cmd\nwhoami\n```"); len(cmds) != 1 {
		t.Fatalf("обычный блок перестал исполняться: %v", cmds)
	}

	// Блок с отступом (внутри списка) — тоже блок, а не текст.
	indented := "Шаги:\n\n  ```cmd-manual\n  bcdboot C:\\Windows /f UEFI\n  ```\n"
	if cmds := extractCommands(indented); len(cmds) != 0 {
		t.Fatalf("блок с отступом исполнен: %v", cmds)
	}
}

// Повтор команды, которую политика уже отклонила дважды, не выполняется вовсе.
func TestGuardBlocksRepeatedRefusal(t *testing.T) {
	g := newCommandGuard()
	cmd := "bcdboot C:\\Windows /f UEFI"

	if stop, _ := g.refused(cmd, "нет в белом списке уровня L1 (нужен L3)"); stop {
		t.Fatal("остановка на первом отказе — рано")
	}
	if blocked, _ := g.check(cmd); blocked {
		t.Fatal("после одного отказа команда ещё допустима (вариация)")
	}
	if stop, _ := g.refused(cmd, "нет в белом списке уровня L1 (нужен L3)"); stop {
		t.Fatal("два разных отказа — ещё не серия из трёх")
	}
	blocked, note := g.check(cmd)
	if !blocked {
		t.Fatal("после двух отказов повтор должен блокироваться")
	}
	if !strings.Contains(note, "2 раз") {
		t.Errorf("в причине нет числа отказов: %q", note)
	}

	// Та же команда другим регистром и с лишними пробелами — тот же повтор.
	if blocked, _ := g.check("  BCDBOOT   c:\\windows /F uefi "); !blocked {
		t.Error("повтор в другом регистре не распознан")
	}
}

// Отказ оператора (не подтвердил y/n) — не политика: команда не считается
// отклонённой и серия сбрасывается.
func TestGuardOperatorCancelDoesNotCount(t *testing.T) {
	g := newCommandGuard()
	cmd := "ipconfig /flushdns"
	g.refused(cmd, "оператор отказался")
	g.refused(cmd, "оператор не подтвердил запуск")
	if blocked, _ := g.check(cmd); blocked {
		t.Fatal("отказ человека принят за запрет политики")
	}

	g.refused("wevtutil cl System", "запрещена политикой уровня L2")
	g.refused(cmd, "оператор отказался") // серия прервана человеком
	if stop, _ := g.refused("diskpart", "нет в белом списке уровня L1 (нужен L3)"); stop {
		t.Fatal("серия не должна продолжаться после отказа оператора")
	}
}

// Три отказа подряд — стоп цепочки; выполненная команда серию сбрасывает.
func TestGuardStopsAfterStreak(t *testing.T) {
	g := newCommandGuard()
	g.beginTurn()
	refuse := func(cmd string) bool {
		stop, _ := g.refused(cmd, "запрещена политикой уровня L1")
		return stop
	}

	if refuse("bcdedit /enum") || refuse("diskpart") {
		t.Fatal("остановка раньше третьего отказа")
	}
	if !refuse("mountvol") {
		t.Fatal("третий отказ подряд обязан остановить цепочку")
	}

	// Новый ответ модели — новая серия (счётчики повторов при этом живут).
	g.beginTurn()
	if refuse("bcdedit /enum") {
		t.Fatal("серия не сброшена началом нового хода")
	}
	g.ran()
	if refuse("bcdedit /enum all") {
		t.Fatal("выполненная команда не сбросила серию")
	}
}

// Смена уровня и /clear снимают память отказов: на L3 та же команда законна.
func TestGuardReset(t *testing.T) {
	g := newCommandGuard()
	cmd := "bcdboot C:\\Windows /f UEFI"
	g.refused(cmd, "нет в белом списке уровня L1 (нужен L3)")
	g.refused(cmd, "нет в белом списке уровня L1 (нужен L3)")
	if blocked, _ := g.check(cmd); !blocked {
		t.Fatal("подготовка теста: команда должна быть заблокирована")
	}
	g.reset()
	if blocked, _ := g.check(cmd); blocked {
		t.Fatal("после смены уровня повтор снова допустим")
	}
}

// Системный промпт обязан называть механику диалога и метку cmd-manual: без
// этого модель снова начнёт говорить «скажи оператору» и приписывать себе
// чужие запуски.
func TestSystemPromptExplainsDialogue(t *testing.T) {
	saved := currentLevel
	defer func() { currentLevel = saved }()
	currentLevel = L1Read

	p := systemPromptBase()
	for _, needle := range []string{"cmd-manual", "Вывод команды", "НЕ выполнялась"} {
		if !strings.Contains(p, needle) {
			t.Errorf("в системном промпте нет %q", needle)
		}
	}
}

// Отказ политики обязан назвать рычаг: уровень поднимает оператор командой
// /level 3. На прогоне 19.11.2026 (GIGA) модели говорили «нужен L3», но не
// говорили чем — она 28 раз просила «перезапустить от администратора», хотя
// процесс уже был повышен, и цикл не мог закончиться.
func TestSystemPromptNamesLevelLever(t *testing.T) {
	saved := currentLevel
	defer func() { currentLevel = saved }()
	currentLevel = L1Read

	if p := systemPromptBase(); !strings.Contains(p, "/level") || !strings.Contains(p, "L1") {
		t.Errorf("в промпте нет рычага уровня (/level) или текущего уровня L1")
	}
	for _, cmd := range []string{"bcdedit /enum all", "mycustomtool --scan --deep"} {
		res := handleCommand(nil, Config{}, cmd)
		if res.handled {
			t.Fatalf("%q на L1 не должна выполняться", cmd)
		}
		if !strings.Contains(res.note, "/level 3") {
			t.Errorf("отказ по %q не называет /level 3: %q", cmd, res.note)
		}
	}
}
