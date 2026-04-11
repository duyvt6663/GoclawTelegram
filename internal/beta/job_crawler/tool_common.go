package jobcrawler

import (
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func toolJSONResult(data any) *tools.Result {
	encoded, err := json.Marshal(data)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(encoded))
}
