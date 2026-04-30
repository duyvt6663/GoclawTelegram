package gptimageedit

import "strings"

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
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
