package home

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
)

func lightModeTestTargets(t *testing.T) LightModes {
	t.Helper()
	targets := LightModes{
		{Selector: "input_select.living", Match: `(?:торшер\p{L}*|ламп\p{L}*) в гостин\p{L}*`},
		{Selector: "input_select.bedroom", Match: `торшер\p{L}* в спальн\p{L}*|ламп\p{L}* у тумб\p{L}*`},
		{Selector: "input_select.balcony", Match: `(?:люстр\p{L}* на )?балкон\p{L}*`},
	}
	if err := targets.Compile(); err != nil {
		t.Fatal(err)
	}
	return targets
}

func TestLightModeVoiceCommands(t *testing.T) {
	targets := lightModeTestTargets(t)
	for _, tc := range []struct {
		phrase, selector, option, answer string
		hidden, unchanged, already       bool
	}{
		{phrase: "Алиса, поставь торшер в гостиной в режим Чтение", selector: "input_select.living", option: "Чтение", answer: "Выбран режим «Чтение»"},
		{phrase: "Включи режим чтения на торшере в гостиной", selector: "input_select.living", option: "Чтение", answer: "Выбран режим «Чтение»"},
		{phrase: "Поставь режим торшера в спальне на Ночник", selector: "input_select.bedroom", option: "Ночник", answer: "Выбран режим «Ночник»"},
		{phrase: "Включи режим Ночник у лампы у тумбы", selector: "input_select.bedroom", option: "Ночник", answer: "Выбран режим «Ночник»"},
		{phrase: "Поставь люстры на балконе в режим северного сияния", selector: "input_select.balcony", option: "Северное сияние", answer: "Выбран режим «Северное сияние»"},
		{phrase: "Выбери режим лава лампа на балконе", selector: "input_select.balcony", option: "Лава-лампа", answer: "Выбран режим «Лава-лампа»"},
		{phrase: "Включи режим Чтение на торшере", answer: "Укажите светильник и комнату"},
		{phrase: "Поставь торшер в гостиной и лампу в спальне в режим Чтение", answer: "Укажите светильник и комнату"},
		{phrase: "Поставь торшер в гостиной в режим Чтение и Закат", selector: "input_select.living", answer: "Назовите режим из списка"},
		{phrase: "Поставь торшер в гостиной в режим Чтение", selector: "input_select.living", hidden: true, answer: "не разрешён для Assist"},
		{phrase: "Поставь торшер в гостиной в режим Чтение", selector: "input_select.living", option: "Чтение", unchanged: true},
		{phrase: "Поставь торшер в гостиной в режим Чтение", selector: "input_select.living", option: "Чтение", already: true, answer: "уже выбран"},
	} {
		t.Run(tc.phrase, func(t *testing.T) {
			calls := 0
			state := "Вечер"
			if tc.already {
				state = tc.option
			}
			c := Controller{LightModes: targets, Tools: toolFunc(func(_ context.Context, name string, args map[string]any) (jsontext.Value, error) {
				if name == "ha_get_skill_guide" {
					return jsontext.Value(`{"success":true}`), nil
				}
				if tc.selector == "" || args["entity_id"] != tc.selector {
					t.Fatalf("unexpected HA target: %s %+v", name, args)
				}
				switch name {
				case "ha_get_entity_exposure":
					return json.Marshal(map[string]any{"exposed_to": map[string]bool{"conversation": !tc.hidden}})
				case "ha_get_state":
					return json.Marshal(map[string]any{"data": map[string]any{"state": state, "attributes": map[string][]string{"options": {"Вечер", "Чтение", "Ночник", "Северное сияние", "Лава-лампа"}}}})
				case "ha_call_service":
					calls++
					data, _ := args["data"].(map[string]any)
					if args["domain"] != "input_select" || args["service"] != "select_option" || data["option"] != tc.option {
						t.Fatalf("wrong HA mode action: %+v", args)
					}
					if !tc.unchanged {
						state = tc.option
					}
					return jsontext.Value(`{"success":true}`), nil
				default:
					t.Fatalf("unexpected HA tool %s", name)
					return nil, nil
				}
			})}
			answer, err := c.Handle(t.Context(), tc.phrase)
			wantCalls := 0
			if tc.option != "" && !tc.already {
				wantCalls = 1
			}
			if calls != wantCalls || (err != nil) != tc.unchanged || !strings.Contains(answer, tc.answer) {
				t.Fatalf("calls=%d want=%d answer=%q err=%v", calls, wantCalls, answer, err)
			}
		})
	}
}

func TestLightModeDoesNotTakeOverOtherCommands(t *testing.T) {
	targets := lightModeTestTargets(t)
	for _, phrase := range []string{
		"Включи свет в гостиной", "Поставь кондиционер в гостиной в режим охлаждения",
		"Как включить режим Чтение на торшере?", "Не включай режим Чтение на торшере в гостиной",
	} {
		if _, _, ok := targets.command(phrase); ok {
			t.Errorf("treated %q as a light mode action", phrase)
		}
	}
}

func TestLightModeRejectsAmbiguousOption(t *testing.T) {
	if mode := availableLightMode("чтения", []string{"Чтение", "Чтению"}); mode != "" {
		t.Fatalf("picked an ambiguous mode %q", mode)
	}
}
