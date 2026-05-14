package zlibrarymcp

import (
	"context"
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *ZLibraryMCPFeature, router *gateway.MethodRouter) {
	router.Register("beta.zlibrary_mcp.public_key", feature.handlePublicKeyMethod)
	router.Register("beta.zlibrary_mcp.status", feature.handleStatusMethod)
	router.Register("beta.zlibrary_mcp.search", feature.handleSearchMethod)
	router.Register("beta.zlibrary_mcp.download", feature.handleDownloadMethod)
}

func (f *ZLibraryMCPFeature) handlePublicKeyMethod(_ context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	payload, err := f.publicKey()
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *ZLibraryMCPFeature) handleStatusMethod(_ context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	client.SendResponse(protocol.NewOKResponse(req.ID, f.status()))
}

func (f *ZLibraryMCPFeature) handleSearchMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params SearchBooksRequest
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	payload, err := f.searchBooks(ctx, tenantKeyFromCtx(ctx), params)
	if err != nil {
		sendMethodError(client, req, err)
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *ZLibraryMCPFeature) handleDownloadMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	params, err := decodeDownloadRequest(req.Params)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	payload, err := f.downloadBook(ctx, tenantKeyFromCtx(ctx), params)
	if err != nil {
		sendMethodError(client, req, err)
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func sendMethodError(client *gateway.Client, req *protocol.RequestFrame, err error) {
	code := protocol.ErrInternal
	if isInputError(err) {
		code = protocol.ErrInvalidRequest
	}
	client.SendResponse(protocol.NewErrorResponse(req.ID, code, err.Error()))
}
