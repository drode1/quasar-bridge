package home

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"testing"
)

func TestKettleVoiceCommands(t *testing.T) {
	scripts := &KettleScripts{Stop: "script.kettle_stop", Heat: "script.kettle_heat", Boil: "script.kettle_boil"}
	for _, tc := range []struct {
		phrase, service string
		temperature     int
		hidden          bool
	}{
		{phrase: "Алиса, выключи чайник.", service: "kettle_stop"},
		{phrase: "Останови чайник на кухне", service: "kettle_stop"},
		{phrase: "Поставь чаник на 70 градусов", service: "kettle_heat", temperature: 70},
		{phrase: "Нагрей чайник до восьмидесяти пяти градусов", service: "kettle_heat", temperature: 85},
		{phrase: "Поставь чайник на 99 °C, пожалуйста!", service: "kettle_heat", temperature: 99},
		{phrase: "Поставь чайник на кипячение", service: "kettle_boil"},
		{phrase: "Вскипяти чайник", service: "kettle_boil", hidden: true},
		{phrase: "Поставь чайник на 100 градусов"},
		{phrase: "Поставь чайник на 70,5 градуса"},
		{phrase: "Вскипяти чайник и выключи свет"},
	} {
		t.Run(tc.phrase, func(t *testing.T) {
			calls := 0
			c := Controller{Kettle: scripts, Tools: toolFunc(func(_ context.Context, name string, args map[string]any) (jsontext.Value, error) {
				switch name {
				case "ha_get_skill_guide":
					return jsontext.Value(`{"success":true}`), nil
				case "ha_get_entity_exposure":
					if args["entity_id"] != "script."+tc.service {
						t.Fatalf("checked exposure of %v, want script.%s", args["entity_id"], tc.service)
					}
					if tc.hidden {
						return jsontext.Value(`{"exposed_to":{"conversation":false}}`), nil
					}
					return jsontext.Value(`{"exposed_to":{"conversation":true}}`), nil
				case "ha_call_service":
					calls++
					data, _ := args["data"].(map[string]any)
					if args["domain"] != "script" || args["service"] != tc.service ||
						(tc.temperature > 0 && data["temperature"] != tc.temperature) ||
						(tc.temperature == 0 && len(data) != 0) {
						t.Fatalf("wrong HA script call: %+v", args)
					}
					return jsontext.Value(`{"success":true}`), nil
				default:
					t.Fatalf("unexpected HA tool call %s", name)
					return nil, nil
				}
			})}
			answer, err := c.Handle(t.Context(), tc.phrase)
			want := 0
			if tc.service != "" && !tc.hidden {
				want = 1
			}
			if err != nil || calls != want || strings.Contains(answer, "отправлена") != (want == 1) {
				t.Fatalf("calls=%d want=%d answer=%q err=%v", calls, want, answer, err)
			}
		})
	}
}

func TestKettleDoesNotTreatMentionsAsCommands(t *testing.T) {
	for _, phrase := range []string{"Как вскипятить чайник?", "Не вскипяти чайник", "Чайник уже закипел?"} {
		if _, _, ok := parseKettleCommand(phrase); ok {
			t.Errorf("treated %q as a command", phrase)
		}
	}
}
