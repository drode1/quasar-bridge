package home

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// KettleScripts binds explicit kettle commands to three opt-in HA scripts.
type KettleScripts struct {
	Stop string `json:"stop"`
	Heat string `json:"heat"`
	Boil string `json:"boil"`
}

var scriptEntityID = regexp.MustCompile(`^script\.[a-z0-9_]+$`)

func (s *KettleScripts) Validate() error {
	if s == nil {
		return nil
	}
	if !scriptEntityID.MatchString(s.Stop) || !scriptEntityID.MatchString(s.Heat) || !scriptEntityID.MatchString(s.Boil) {
		return errors.New("home_kettle_scripts requires stop, heat and boil script entity IDs")
	}
	if s.Stop == s.Heat || s.Stop == s.Boil || s.Heat == s.Boil {
		return errors.New("home_kettle_scripts must use three distinct scripts")
	}
	return nil
}

type kettleOperation uint8

const (
	kettleClarify kettleOperation = iota
	kettleStop
	kettleHeat
	kettleBoil
)

var kettlePhrase = regexp.MustCompile(`^(?:алиса[\s,:]+)?(?:пожалуйста[\s,:]+)?(выключи(?:ть)?|отключи(?:ть)?|останови(?:ть)?|стоп|вскипяти(?:ть)?|закипяти|нагрей|нагреть|поставь|поставить|установи(?:ть)?)\s+ча(?:й)?ник(?:\s+(.*))?$`)
var kettleDegrees = regexp.MustCompile(`(?:\s*°(?:\s*[cс])?|\s+градус(?:а|ов)?(?:\s+цельсия)?)$`)

func parseKettleCommand(text string) (kettleOperation, int, bool) {
	s := strings.TrimRight(strings.TrimSpace(strings.ToLower(text)), " .!?")
	s = strings.TrimRight(strings.TrimSuffix(s, " пожалуйста"), " ,")
	match := kettlePhrase.FindStringSubmatch(s)
	if match == nil {
		return kettleClarify, 0, false
	}
	args := strings.TrimPrefix(strings.TrimSuffix(match[2], " на кухне"), "на кухне ")
	if args == "на кухне" {
		args = ""
	}
	switch match[1] {
	case "выключи", "выключить", "отключи", "отключить", "останови", "остановить", "стоп":
		if args == "" {
			return kettleStop, 0, true
		}
	case "вскипяти", "вскипятить", "закипяти":
		if args == "" {
			return kettleBoil, 0, true
		}
	default:
		value, ok := strings.CutPrefix(args, "на ")
		if !ok {
			value, ok = strings.CutPrefix(args, "до ")
		}
		if ok {
			if value == "кипячение" || value == "кипяток" {
				return kettleBoil, 0, true
			}
			if unit := kettleDegrees.FindStringIndex(value); unit != nil {
				value = value[:unit[0]]
			}
			if n := kettleTemperature(strings.TrimSpace(value)); n >= 40 && n <= 99 {
				return kettleHeat, n, true
			}
		}
	}
	return kettleClarify, 0, true
}

var kettleTens = map[string]int{
	"сорок": 40, "сорока": 40, "пятьдесят": 50, "пятидесяти": 50, "шестьдесят": 60, "шестидесяти": 60,
	"семьдесят": 70, "семидесяти": 70, "восемьдесят": 80, "восьмидесяти": 80, "девяносто": 90, "девяноста": 90,
}
var kettleUnits = map[string]int{
	"один": 1, "одного": 1, "два": 2, "двух": 2, "три": 3, "трёх": 3, "трех": 3, "четыре": 4, "четырёх": 4, "четырех": 4,
	"пять": 5, "пяти": 5, "шесть": 6, "шести": 6, "семь": 7, "семи": 7, "восемь": 8, "восьми": 8, "девять": 9, "девяти": 9,
}

func kettleTemperature(value string) int {
	if n, err := strconv.Atoi(value); err == nil {
		return n
	}
	tens, units, more := strings.Cut(value, " ")
	n := kettleTens[tens]
	if !more || n == 0 {
		return n
	}
	if unit := kettleUnits[strings.TrimSpace(units)]; unit != 0 {
		return n + unit
	}
	return 0
}

func (c *Controller) runKettle(ctx context.Context, op kettleOperation, temperature int) (string, error) {
	var id, answer string
	switch op {
	case kettleStop:
		id, answer = c.Kettle.Stop, "Команда остановки чайника отправлена."
	case kettleHeat:
		id, answer = c.Kettle.Heat, fmt.Sprintf("Команда нагрева чайника до %d градусов отправлена.", temperature)
	case kettleBoil:
		id, answer = c.Kettle.Boil, "Команда кипячения отправлена."
	default:
		return "Скажите «Выключи чайник», «Вскипяти чайник» или укажите целую температуру от 40 до 99 градусов.", nil
	}
	visible, err := c.exposed(ctx, id, false)
	if err != nil {
		return "", err
	}
	if !visible {
		return "Сценарий чайника не разрешён для Assist в Home Assistant.", nil
	}
	// A script's own action accepts named fields and waits for the sequence.
	// Do not make script.turn_on available to the planner for arbitrary targets.
	args := map[string]any{"domain": "script", "service": strings.TrimPrefix(id, "script."), "wait": false}
	if op == kettleHeat {
		args["data"] = map[string]any{"temperature": temperature}
	}
	if _, err := c.Tools.Call(ctx, "ha_call_service", args); err != nil {
		return "", fmt.Errorf("home kettle action failed; no cloud retry: %w", err)
	}
	return answer, nil
}
