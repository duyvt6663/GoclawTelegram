package gptimageedit

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type editTool struct {
	feature *GPTImageEditFeature
}

func (t *editTool) Name() string { return toolName }

func (t *editTool) Description() string {
	return "Edit an attached or workspace image with OpenAI GPT Image. Supports object removal, replacement/inpainting, style transfer, background changes, text edits, and upscaling."
}

func (t *editTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Natural language edit instruction for the image.",
			},
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"auto", "remove_object", "replace_object", "inpaint", "style_transfer", "background_change", "text_edit", "upscale"},
				"description": "Optional edit category for tracing. The prompt remains the source of truth.",
			},
			"image_path": map[string]any{
				"type":        "string",
				"description": "Optional path to a png, jpg, or webp image. If omitted, the latest attached chat image is used.",
			},
			"output_format": map[string]any{
				"type":        "string",
				"enum":        []string{"png", "jpeg", "webp"},
				"description": "Output image format. Defaults to png.",
			},
			"size": map[string]any{
				"type":        "string",
				"description": "Optional OpenAI image size, for example auto or 1024x1024.",
			},
			"quality": map[string]any{
				"type":        "string",
				"enum":        []string{"low", "medium", "high"},
				"description": "Optional quality setting.",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *editTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("GPT image edit feature is unavailable")
	}

	payload, err := t.feature.edit(ctx, EditRequest{
		Prompt:       stringArg(args, "prompt"),
		Operation:    stringArg(args, "operation"),
		ImagePath:    stringArg(args, "image_path"),
		OutputFormat: stringArg(args, "output_format"),
		Size:         stringArg(args, "size"),
		Quality:      stringArg(args, "quality"),
		Source:       "tool",
		Channel:      tools.ToolChannelFromCtx(ctx),
		ChatID:       tools.ToolChatIDFromCtx(ctx),
	}, false)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	result := &tools.Result{
		ForLLM: fmt.Sprintf(
			"MEDIA:%s\nEdited image created with %s. Use the exact filename when referencing it: %s",
			payload.OutputPath,
			payload.Model,
			filepath.Base(payload.OutputPath),
		),
		Deliverable: fmt.Sprintf("[Edited image: %s]\nModel: %s\nOperation: %s", filepath.Base(payload.OutputPath), payload.Model, payload.Operation),
	}
	result.Media = []bus.MediaFile{{Path: payload.OutputPath, MimeType: payload.OutputMIME}}
	return result
}
