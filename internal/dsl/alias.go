package dsl

import (
	"context"
	"errors"
	"fmt"

	"pocketlab/internal/device"
)

// actionAliases maps every documented action name or alias (normalized: no
// case, underscores, hyphens or spaces) to Pocketlab's canonical action.
var actionAliases = buildAliases(map[string][]string{
	"launch_app":            {"open_app", "launch_app", "start_app", "app"},
	"stop_app":              {"kill_app", "terminate_app", "close_app", "stop_app", "force_stop"},
	"open_url":              {"open_link", "open_url", "openurl", "deeplink", "open_deeplink", "open_deep_link"},
	"tap":                   {"tap", "tap_on", "click", "touch"},
	"double_tap":            {"double_tap", "double_tap_on"},
	"long_press":            {"long_press", "long_press_on", "long_tap"},
	"two_finger_tap":        {"two_finger_tap"},
	"pinch":                 {"pinch"},
	"swipe":                 {"swipe"},
	"scroll":                {"scroll"},
	"drag":                  {"drag"},
	"drag_path":             {"drag_path"},
	"perform_actions":       {"perform_actions", "w3c_actions", "perform_gesture"},
	"type":                  {"type", "input_text", "type_text", "enter_text", "input", "set_text"},
	"type_secret":           {"type_secret", "type_password", "input_secret", "type_credential"},
	"clear":                 {"clear", "clear_text", "clear_field", "erase_text", "erase"},
	"dismiss_keyboard":      {"hide_keyboard", "dismiss_keyboard"},
	"copy_text":             {"copy_text", "copy_text_from"},
	"paste_text":            {"paste_text", "paste"},
	"set_clipboard":         {"set_clipboard"},
	"press_key":             {"press_key", "key_press"},
	"navigate":              {"navigate"},
	"toggle":                {"toggle"},
	"select_picker":         {"select_picker", "pick", "set_picker", "picker_select", "select_wheel"},
	"observe":               {"observe"},
	"audit":                 {"audit"},
	"screenshot":            {"screenshot", "take_screenshot", "capture_screenshot"},
	"record_start":          {"record_start"},
	"record_stop":           {"record_stop"},
	"wait_for":              {"wait_for", "extended_wait_until", "wait_for_element", "wait_until_visible"},
	"wait_stable":           {"wait_stable"},
	"wait":                  {"wait", "delay", "sleep", "pause"},
	"checkpoint":            {"checkpoint"},
	"set_location":          {"set_location"},
	"reset_location":        {"reset_location"},
	"set_permissions":       {"set_permissions"},
	"grant_permission":      {"grant_permission"},
	"revoke_permission":     {"revoke_permission"},
	"biometrics":            {"biometrics", "biometric", "face_id", "touch_id", "fingerprint"},
	"fold":                  {"fold", "set_fold", "fold_device"},
	"siri":                  {"siri"},
	"metrics_start":         {"metrics_start"},
	"metrics_stop":          {"metrics_stop"},
	"extract":               {"extract"},
	"eval_script":           {"eval_script", "eval"},
	"run_script":            {"run_script", "script"},
	"request_user_input":    {"request_user_input"},
	"select_web_context":    {"select_web_context"},
	"execute_js":            {"execute_js"},
	"install_app":           {"install_app"},
	"uninstall_app":         {"uninstall_app"},
	"scroll_until_visible":  {"scroll_until_visible"},
	"assert_exists":         {"assert_exists", "assert", "assert_visible", "assert_present"},
	"assert_not_exists":     {"assert_not_exists", "assert_not_visible", "assert_absent", "assert_hidden"},
	"assert_count":          {"assert_count"},
	"assert_property":       {"assert_property"},
	"assert_screen_changed": {"assert_screen_changed"},
	"if_exists":             {"if_exists"},
	"repeat":                {"repeat"},
	"ocr_tap":               {"ocr_tap", "tap_text_ocr"},
	"assert_text_ocr":       {"assert_text_ocr", "wait_for_text_ocr"},
	"ocr":                   {"ocr", "read_screen_text"},
	"web_navigate":          {"web_navigate"},
	"web_click":             {"web_click"},
	"web_type":              {"web_type"},
	"web_eval":              {"web_eval"},
	"web_wait":              {"web_wait"},
	"web_assert_text":       {"web_assert_text"},
})

func buildAliases(m map[string][]string) map[string]string {
	out := map[string]string{}
	for canon, names := range m {
		out[normKey(canon)] = canon
		for _, n := range names {
			out[normKey(n)] = canon
		}
	}
	return out
}

// canonicalAction resolves an action name or alias to the canonical action.
func canonicalAction(name string) (string, bool) {
	c, ok := actionAliases[normKey(name)]
	return c, ok
}

// CanonicalAction resolves an action name or alias (case, underscores and
// hyphens are ignored) to Pocketlab's canonical action name.
func CanonicalAction(name string) (string, bool) { return canonicalAction(name) }

// ActionNames returns every accepted action name as (alias -> canonical),
// using the canonical spelling of each documented name.
func ActionNames() map[string]string {
	out := map[string]string{}
	for k, v := range actionAliases {
		out[k] = v
	}
	return out
}

// stepError is an error carrying a MobAI-style error code.
type stepError struct {
	code string
	err  error
}

func (e *stepError) Error() string { return e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func codeErr(code string, format string, args ...any) error {
	return &stepError{code: code, err: fmt.Errorf(format, args...)}
}

func withCode(code string, err error) error {
	if err == nil {
		return nil
	}
	var se *stepError
	if errors.As(err, &se) {
		return err
	}
	return &stepError{code: code, err: err}
}

func errWeb() error {
	return codeErr(CodeContextNotAvail, "%s", webNotSupportedPhrase)
}

func classify(err error) string {
	var se *stepError
	switch {
	case errors.As(err, &se):
		return se.code
	case errors.Is(err, device.ErrElementNotFound):
		return CodeNoMatch
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	}
	return CodeExecutionError
}
