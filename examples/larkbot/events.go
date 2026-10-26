package main

import (
	"context"
	"log/slog"
	"net/http"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// newDispatcher routes im.message.receive_v1 to the bot. Empty token and key
// mean no verification and no decryption (the long connection needs neither;
// the fake Lark sends plain events).
func newDispatcher(b *bot) *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(b.onMessage)
}

// larkEventHandler serves Lark's HTTP event callback (-lark-events http)
// through the SDK's webhook adapter, into the same handler as the long connection.
func larkEventHandler(b *bot) http.HandlerFunc {
	return httpserverext.NewEventHandlerFunc(newDispatcher(b))
}

// runLongConnection receives events over Lark's long connection until ctx ends.
func runLongConnection(ctx context.Context, log *slog.Logger, appID, appSecret string, b *bot) error {
	ws := larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(newDispatcher(b)),
		larkws.WithLogger(larkLogger{log, []string{appSecret}}),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithOnReady(func() { log.Info("lark long connection ready") }),
		larkws.WithOnError(func(err error) { log.Error("lark long connection", "err", err) }),
		larkws.WithOnDisconnected(func() { log.Warn("lark long connection lost") }),
	)
	return ws.Start(ctx) // blocks until ctx ends or the connection cannot be kept
}
