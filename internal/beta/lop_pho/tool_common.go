package loppho

import (
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func toolJSONResult(data any) *tools.Result {
	out, _ := json.Marshal(data)
	return tools.NewResult(string(out))
}
