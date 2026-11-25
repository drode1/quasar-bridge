package home

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// LightModes opts specific HA selectors into explicit voice commands.
type LightModes []LightModeTarget

type LightModeTarget struct {
	Selector string `json:"selector"`
	Match    string `json:"match"`
	pattern  *regexp.Regexp
}

var inputSelectEntityID = regexp.MustCompile(`^input_select\.[a-z0-9_]+$`)

func (targets LightModes) Compile() error {
	for i := range targets {
		target := &targets[i]
		if !inputSelectEntityID.MatchString(target.Selector) {
			return fmt.Errorf("home_light_modes target %d requires an input_select entity ID", i+1)
		}
		for j := range targets[:i] {
			if targets[j].Selector == target.Selector {
				return errors.New("home_light_modes selectors must be unique")
			}
		}
		pattern, err := regexp.Compile("(?i)^(?:" + target.Match + ")$")
		if err != nil || pattern.MatchString("") {
			return fmt.Errorf("home_light_modes target %d has an invalid match expression", i+1)
		}
		target.pattern = pattern
	}
	return nil
}

var lightModeVerb = regexp.MustCompile(`(?i)^\s*(?:алиса[\s,:]+)?(?:пожалуйста[\s,:]+)?(?:включи(?:ть)?|поставь|поставить|установи(?:ть)?|выбери|выбрать|переключи(?:ть)?|смени(?:ть)?|измени(?:ть)?|поменяй|поменять|сделай)\s+`)
var lightModeMarker = regexp.MustCompile(`(?:^| )(?:(?:в|на) )?режим(?:а|е|у|ом)?(?: |$)`)
var lightModeNoun = regexp.MustCompile(`(?:^| )(?:торшер|ламп|люстр|свет)\p{L}*(?:[ ,.!?]|$)`)

func (targets LightModes) command(text string) (selector, mode string, handled bool) {
	if len(targets) == 0 {
		return "", "", false
	}
	prefix := lightModeVerb.FindStringIndex(text)
	if prefix == nil {
		return "", "", false
	}
	body := strings.ToLower(strings.TrimRight(strings.TrimSpace(text[prefix[1]:]), " .!?"))
	if strings.Contains(body, "  ") || strings.ContainsAny(body, "\t\r\n") {
		body = strings.Join(strings.Fields(body), " ")
	}
	body = strings.TrimRight(strings.TrimSuffix(body, " пожалуйста"), " ,")
	marker := lightModeMarker.FindStringIndex(body)
	if marker == nil {
		return "", "", false
	}

	ambiguous := false
	consider := func(phrase, option string) {
		phrase = strings.Trim(phrase, " ,")
		head, tail, _ := strings.Cut(phrase, " ")
		switch head {
		case "в", "на", "у", "для":
			phrase = tail
		}
		for _, target := range targets {
			if target.pattern == nil || !target.pattern.MatchString(phrase) {
				continue
			}
			if selector != "" && (selector != target.Selector || mode != option) {
				ambiguous = true
			}
			selector, mode = target.Selector, option
		}
	}
	before, after := body[:marker[0]], body[marker[1]:]
	if before != "" {
		// «Поставь торшер в гостиной в режим Чтение».
		consider(before, strings.TrimPrefix(after, "на "))
	} else {
		// «Включи режим Чтение на торшере» / «Поставь режим торшера на Чтение».
		for rest := after; rest != ""; {
			at := len(after) - len(rest)
			word, tail, _ := strings.Cut(rest, " ")
			if at > 0 && tail != "" {
				switch word {
				case "на", "в", "у", "для":
					consider(tail, after[:at-1])
					if word == "на" {
						consider(after[:at-1], tail)
					}
				}
			}
			rest = tail
		}
	}
	if ambiguous {
		return "", "", true
	}
	return selector, mode, selector != "" || lightModeNoun.MatchString(body)
}

var lightModeReplacer = strings.NewReplacer("ё", "е", "-", " ", "‑", " ", "–", " ")

func lightModeText(s string) string {
	return lightModeReplacer.Replace(strings.ToLower(strings.Trim(s, " \t,.!?«»\"“”")))
}

// Recognize common Russian case endings, not approximate spelling. The result
// must still be unique in the selector's actual options.
func lightModeStem(s string) string {
	for _, ending := range [...]string{"ого", "его", "ому", "ему", "ием", "ией", "ами", "ями", "ыми", "ими", "ое", "ее", "ые", "ие", "ая", "яя", "ую", "юю", "ия", "ии", "ию", "ой", "ей", "ам", "ям", "ом", "ем", "ах", "ях", "а", "я", "ы", "и", "е", "о", "у", "ю", "ь"} {
		if base, ok := strings.CutSuffix(s, ending); ok && len(base) >= 6 {
			return base
		}
	}
	return s
}

func sameLightModeWords(a, b string) bool {
	for {
		aw, ar, am := strings.Cut(a, " ")
		bw, br, bm := strings.Cut(b, " ")
		if am != bm || (aw != bw && lightModeStem(aw) != lightModeStem(bw)) {
			return false
		}
		if !am {
			return true
		}
		a, b = ar, br
	}
}

func availableLightMode(request string, options []string) string {
	want := lightModeText(request)
	if want == "" {
		return ""
	}
	var exact, inflected string
	var duplicateExact, duplicateInflected bool
	for _, option := range options {
		switch normalized := lightModeText(option); {
		case normalized == want:
			duplicateExact = duplicateExact || exact != ""
			exact = option
		case sameLightModeWords(normalized, want):
			duplicateInflected = duplicateInflected || inflected != ""
			inflected = option
		}
	}
	if duplicateExact {
		return ""
	}
	if exact != "" {
		return exact
	}
	if duplicateInflected {
		return ""
	}
	return inflected
}

type lightModeState struct {
	State      string `json:"state"`
	Attributes struct {
		Options []string `json:"options"`
	} `json:"attributes"`
}

func (c *Controller) readLightMode(ctx context.Context, selector string, withOptions bool) (lightModeState, error) {
	args := map[string]any{"entity_id": selector, "fields": []string{"state"}}
	if withOptions {
		args["fields"] = []string{"state", "attributes"}
		args["attribute_keys"] = []string{"options"}
	}
	var result struct {
		Data lightModeState `json:"data"`
	}
	raw, err := c.Tools.Call(ctx, "ha_get_state", args)
	if err != nil {
		return result.Data, err
	}
	if json.Unmarshal(raw, &result) != nil || result.Data.State == "" {
		return result.Data, errors.New("invalid home light mode state")
	}
	return result.Data, nil
}

func (c *Controller) runLightMode(ctx context.Context, selector, requested string) (string, error) {
	if selector == "" {
		return "Укажите светильник и комнату в команде, например «торшер в гостиной».", nil
	}
	visible, err := c.exposed(ctx, selector, false)
	if err != nil {
		return "", err
	}
	if !visible {
		return "Селектор режима не разрешён для Assist в Home Assistant.", nil
	}
	current, err := c.readLightMode(ctx, selector, true)
	if err != nil {
		return "", err
	}
	if current.State == "unknown" || current.State == "unavailable" || len(current.Attributes.Options) == 0 {
		return "Селектор режима сейчас недоступен.", nil
	}
	option := availableLightMode(requested, current.Attributes.Options)
	if option == "" {
		return "Назовите режим из списка этого светильника.", nil
	}
	if current.State == option {
		return "Режим «" + option + "» уже выбран.", nil
	}
	if _, err := c.Tools.Call(ctx, "ha_call_service", map[string]any{
		"domain": "input_select", "service": "select_option", "entity_id": selector,
		"data": map[string]any{"option": option}, "wait": true,
	}); err != nil {
		return "", fmt.Errorf("home light mode failed; no cloud retry: %w", err)
	}
	confirmed, err := c.readLightMode(ctx, selector, false)
	if err != nil {
		return "", err
	}
	if confirmed.State != option {
		return "", errors.New("home light mode was not confirmed by selector state")
	}
	return "Выбран режим «" + option + "».", nil
}
