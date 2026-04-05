package stt

import (
	"context"
	"fmt"
	"log"

	"github.com/streamcoreai/server/internal/config"

	clientinterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/client/interfaces"
	websocketv1 "github.com/deepgram/deepgram-go-sdk/v3/pkg/client/listen/v1/websocket"

	msginterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/api/listen/v1/websocket/interfaces"
)

// deepgramCallback implements the Deepgram SDK's LiveMessageCallback interface.
type deepgramCallback struct {
	onResult func(TranscriptResult)
}

func (cb *deepgramCallback) Open(or *msginterfaces.OpenResponse) error {
	log.Println("[stt] Deepgram connection opened")
	return nil
}

func (cb *deepgramCallback) Message(mr *msginterfaces.MessageResponse) error {
	if len(mr.Channel.Alternatives) == 0 {
		return nil
	}

	text := mr.Channel.Alternatives[0].Transcript
	if text == "" {
		return nil
	}

	cb.onResult(TranscriptResult{
		Text:    text,
		IsFinal: mr.IsFinal || mr.SpeechFinal,
	})
	return nil
}

func (cb *deepgramCallback) Metadata(md *msginterfaces.MetadataResponse) error {
	return nil
}

func (cb *deepgramCallback) SpeechStarted(ssr *msginterfaces.SpeechStartedResponse) error {
	return nil
}

func (cb *deepgramCallback) UtteranceEnd(ur *msginterfaces.UtteranceEndResponse) error {
	return nil
}

func (cb *deepgramCallback) Close(cr *msginterfaces.CloseResponse) error {
	log.Println("[stt] Deepgram connection closed")
	return nil
}

func (cb *deepgramCallback) Error(er *msginterfaces.ErrorResponse) error {
	log.Printf("[stt] Deepgram error: %s", er.ErrMsg)
	return nil
}

func (cb *deepgramCallback) UnhandledEvent(byData []byte) error {
	return nil
}

// deepgramClient wraps the official Deepgram Go SDK websocket client.
type deepgramClient struct {
	ws     *websocketv1.WSCallback
	cancel context.CancelFunc
}

func NewDeepgramClient(ctx context.Context, cfg config.DeepgramConfig, onResult func(TranscriptResult)) (*deepgramClient, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	tOptions := &clientinterfaces.LiveTranscriptionOptions{
		Model:          cfg.Model,
		Encoding:       "linear16",
		SampleRate:     16000,
		Channels:       1,
		Punctuate:      true,
		InterimResults: true,
		Endpointing:    "300",
		VadEvents:      true,
	}

	cb := &deepgramCallback{onResult: onResult}

	ws, err := websocketv1.NewUsingCallbackWithCancel(sttCtx, cancel, cfg.APIKey, &clientinterfaces.ClientOptions{
		EnableKeepAlive: true,
	}, tOptions, cb)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("deepgram client: %w", err)
	}

	if !ws.Connect() {
		cancel()
		return nil, fmt.Errorf("deepgram: failed to connect")
	}

	log.Println("[stt] connected to Deepgram")
	return &deepgramClient{ws: ws, cancel: cancel}, nil
}

// SendAudio sends raw linear16 PCM bytes to Deepgram.
func (c *deepgramClient) SendAudio(data []byte) error {
	_, err := c.ws.Write(data)
	return err
}

// Close gracefully shuts down the Deepgram connection.
func (c *deepgramClient) Close() {
	if c.ws != nil {
		c.ws.Finish()
	}
	c.cancel()
	log.Println("[stt] closed")
}
