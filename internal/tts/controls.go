package tts

import (
	"context"
	"strings"
)

// VoiceControls carries per-utterance delivery hints from the conversation
// layer to the TTS provider — the difference between a flat robotic
// "sorry, I didn't catch that" and a warm, slightly slower one. Zero value
// means "provider defaults".
type VoiceControls struct {
	// Speed is a playback-rate multiplier (1.0 = normal). Zero = unset.
	// Providers clamp to their own safe range.
	Speed float64
	// Tone is the normalized tag the LLM emitted ("warm", "empathetic",
	// …). Providers with richer expressiveness settings may map it beyond
	// speed (e.g. ElevenLabs stability).
	Tone string
}

// IsZero reports whether no controls were requested.
func (vc VoiceControls) IsZero() bool {
	return vc.Speed == 0 && vc.Tone == ""
}

// ControllableStreamer is the optional capability interface for providers
// (and wrappers) that accept per-utterance voice controls. Callers
// type-assert and fall back to plain SynthesizeStream when unsupported, so
// adding a provider never requires touching every Client implementation.
type ControllableStreamer interface {
	SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error)
}

// voiceTags is the inline-tag vocabulary the LLM may emit at the start of a
// sentence (taught in the voice-output system prompt). Tags map to delivery
// presets; unknown bracketed text is left untouched so legitimate brackets
// can't be eaten.
var voiceTags = map[string]VoiceControls{
	"warm":       {Speed: 0.95, Tone: "warm"},
	"empathetic": {Speed: 0.9, Tone: "empathetic"},
	"calm":       {Speed: 0.9, Tone: "calm"},
	"slow":       {Speed: 0.85, Tone: "slow"},
	"excited":    {Speed: 1.1, Tone: "excited"},
	"fast":       {Speed: 1.1, Tone: "fast"},
}

// ParseVoiceTag strips one leading [tag] from a sentence and returns the
// matching controls plus the clean sentence. Sentences without a known
// leading tag come back unchanged with zero controls.
func ParseVoiceTag(sentence string) (VoiceControls, string) {
	trimmed := strings.TrimSpace(sentence)
	if !strings.HasPrefix(trimmed, "[") {
		return VoiceControls{}, sentence
	}
	end := strings.Index(trimmed, "]")
	if end <= 1 || end > 16 {
		return VoiceControls{}, sentence
	}
	tag := strings.ToLower(strings.TrimSpace(trimmed[1:end]))
	vc, ok := voiceTags[tag]
	if !ok {
		return VoiceControls{}, sentence
	}
	return vc, strings.TrimSpace(trimmed[end+1:])
}

// StripVoiceTags removes every known [tag] occurrence from text — used when
// recording transcripts so delivery hints never pollute what reviews and
// the LLM history treat as spoken words.
func StripVoiceTags(text string) string {
	if !strings.Contains(text, "[") {
		return text
	}
	for tag := range voiceTags {
		text = strings.ReplaceAll(text, "["+tag+"]", "")
		text = strings.ReplaceAll(text, "["+strings.ToUpper(tag[:1])+tag[1:]+"]", "")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}
