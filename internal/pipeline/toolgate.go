package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"

	"github.com/streamcoreai/streamcore-server/internal/llm"
	"github.com/streamcoreai/streamcore-server/internal/plugin"
)

// gateToolCall applies the plugin confirmation gate before a tool runs.
//
// It returns the arguments the tool should actually see, and — when the call is
// not yet authorised — the JSON challenge to hand straight back to the model
// instead of a result. Tools that do not declare ConfirmationRequired, which is
// every tool that predates the gate, pass through untouched.
func (p *Pipeline) gateToolCall(tool plugin.Tool, args json.RawMessage) (json.RawMessage, string, error) {
	if p.pluginMgr == nil {
		return args, "", nil
	}

	clean, challenge, err := p.pluginMgr.Confirmations().Gate(p.sessionID(), tool, args)
	if err != nil {
		return nil, "", err
	}
	if challenge == nil {
		return clean, "", nil
	}

	encoded, err := json.Marshal(challenge)
	if err != nil {
		return nil, "", err
	}
	log.Printf("[tools] %s requires confirmation; challenged instead of executing", tool.Name())
	return clean, string(encoded), nil
}

// runTool executes a gated call, handing session-aware tools the conversation
// they belong to so two callers can never share one developer task.
//
// Whatever the tool returns passes through the result envelope: a tool may ask
// for packets to go to the device as well as words to go to the model. That is
// what lets locomotion, gestures and anything else that acts on the client be a
// plugin rather than a branch in this file.
func (p *Pipeline) runTool(ctx context.Context, tool plugin.Tool, args json.RawMessage) (string, error) {
	raw, err := p.executeTool(ctx, tool, args)
	if err != nil {
		return "", err
	}

	result := plugin.ParseResult(raw)
	for _, emission := range result.Emit {
		// The model often reaches a conclusion a partial already acted on. The
		// device is already doing this; sending it again would read as a second
		// command and restart the motion in progress.
		if p.reflexAlreadySent(emission) {
			log.Printf("[tools] %s already sent from a partial; not repeating %s",
				tool.Name(), emission.Topic)
			continue
		}
		if err := p.emit(emission); err != nil {
			// The packet is the point of a dispatching tool, so a failure to
			// send it must not be reported to the model as success.
			return "", fmt.Errorf("tool %s: %w", tool.Name(), err)
		}
	}
	return result.Speak, nil
}

func (p *Pipeline) executeTool(ctx context.Context, tool plugin.Tool, args json.RawMessage) (string, error) {
	if scoped, ok := tool.(plugin.SessionTool); ok {
		return scoped.ExecuteInSession(ctx, p.sessionID(), args)
	}
	return tool.Execute(args)
}

// emit writes one topic-addressed packet to the client. The framing is the
// firmware's: a data packet whose payload is base64-encoded JSON, dispatched by
// its on_data handler.
func (p *Pipeline) emit(emission plugin.Emission) error {
	payload := emission.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := p.sendEvent(dcDataPacket{
		Type:    "data",
		Topic:   emission.Topic,
		Payload: base64.StdEncoding.EncodeToString(payload),
	}); err != nil {
		return fmt.Errorf("send %s: %w", emission.Topic, err)
	}
	log.Printf("[tools] emitted %s %s", emission.Topic, payload)
	return nil
}

// Emit satisfies plugin.SessionSink, letting a plugin push a packet at this
// conversation's client without having been asked for one.
func (p *Pipeline) Emit(_ context.Context, emission plugin.Emission) error {
	return p.emit(emission)
}

// Complete satisfies plugin.SessionSink. A plugin that needs a model reaches
// the one the operator already configured, rather than carrying a second API
// key that can drift from the server's.
func (p *Pipeline) Complete(ctx context.Context, req plugin.CompletionRequest) (string, error) {
	client, err := p.completionClient()
	if err != nil {
		return "", err
	}
	return client.OneShot(ctx, req.System, req.Prompt)
}

// Search satisfies plugin.SessionSink. A plugin grounds an answer in the same
// corpus the agent uses, instead of standing up a second retrieval stack whose
// contents can drift from this one's.
func (p *Pipeline) Search(ctx context.Context, query string, limit int) ([]string, error) {
	if p.ragClient == nil {
		return nil, fmt.Errorf("no knowledge base is configured")
	}
	return p.ragClient.Search(ctx, query, limit)
}

// Capture satisfies plugin.SessionSink. It obtains something only the client
// can supply and hands back the fields to merge into a tool's arguments.
//
// This is what replaced hardcoding one plugin's name in the tool handler to
// fetch it a picture. A plugin declares `requires: [camera_frame]` and the next
// one that needs a frame works without the pipeline learning its name.
func (p *Pipeline) Capture(_ context.Context, capability string) (json.RawMessage, error) {
	switch capability {
	case CameraFrameCapability:
		return p.captureCameraFrame()
	default:
		return nil, fmt.Errorf("this client cannot supply %q", capability)
	}
}

// CameraFrameCapability is the name a plugin asks for a still image under.
const CameraFrameCapability = "camera_frame"

func (p *Pipeline) captureCameraFrame() (json.RawMessage, error) {
	log.Printf("[vision] requesting a frame from the client")

	res, err := p.imageRecv.requestAndWait(p.sendEvent)
	if err != nil {
		return nil, err
	}

	captured := map[string]string{"image_base64": res.Base64}
	if res.Mime != "" {
		captured["image_mime"] = res.Mime
	}
	log.Printf("[vision] captured %d bytes of base64", len(res.Base64))

	return json.Marshal(captured)
}

// completionClient returns a client for one-shot work. In realtime mode there
// is no conversation client to borrow, so one is built on demand and kept.
func (p *Pipeline) completionClient() (llm.Client, error) {
	if p.llmClient != nil {
		return p.llmClient, nil
	}

	p.oneShotOnce.Do(func() {
		p.oneShotLLM, p.oneShotErr = llm.NewClient(p.cfg, "")
	})
	if p.oneShotErr != nil {
		return nil, fmt.Errorf("no model available for plugin completion: %w", p.oneShotErr)
	}
	return p.oneShotLLM, nil
}

func (p *Pipeline) sessionID() string {
	if p.conv == nil {
		return ""
	}
	return p.conv.SessionID
}
