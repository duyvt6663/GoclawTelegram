package memereplace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type replaceTool struct {
	feature *MemeReplaceFeature
}

func (t *replaceTool) Name() string { return toolName }

func (t *replaceTool) Description() string {
	return "Replace the green-screen region in the single built-in meme template with the latest attached image. Use for /meme_replace or Vietnamese requests like 'ghép ảnh vào meme này'."
}

func (t *replaceTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_path": map[string]any{
				"type":        "string",
				"description": "Optional path to a png, jpg, webp, or gif image. If omitted, the latest attached chat image is used.",
			},
			"fit_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"cover", "contain"},
				"description": "How to fit the image into the meme green-screen region. Defaults to cover.",
			},
		},
	}
}

func (t *replaceTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("meme_replace feature is unavailable")
	}

	payload, err := t.feature.replace(ctx, ReplaceRequest{
		ImagePath: tools.GetParamString(args, "image_path", ""),
		FitMode:   tools.GetParamString(args, "fit_mode", ""),
		Source:    "tool",
		Channel:   tools.ToolChannelFromCtx(ctx),
		ChatID:    tools.ToolChatIDFromCtx(ctx),
	}, false)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	result := &tools.Result{
		ForLLM: fmt.Sprintf(
			"MEDIA:%s\nCreated meme replacement using template %s. Use the exact filename when referencing it: %s",
			payload.OutputPath,
			payload.TemplateID,
			filepath.Base(payload.OutputPath),
		),
		Deliverable: fmt.Sprintf("[Meme replacement: %s]\nTemplate: %s", filepath.Base(payload.OutputPath), payload.TemplateID),
	}
	result.Media = []bus.MediaFile{{Path: payload.OutputPath, MimeType: payload.OutputMIME}}
	return result
}
