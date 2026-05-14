package gptimageedit

import "strings"

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func stringSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		return compactStringsPreserveOrder(value)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return compactStringsPreserveOrder(out)
	default:
		return nil
	}
}

func isEditInputError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	inputMarkers := []string{
		"prompt is",
		"prompt ",
		"image is",
		"image file too large",
		"unsupported image format",
		"no editable image",
		"invalid base64",
		"image_path",
		"relative image_path",
		"attach a",
	}
	for _, marker := range inputMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
