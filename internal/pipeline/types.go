package pipeline

import "time"

// PCMFrame is a single audio frame flowing through the pipeline.
// 20ms at 16kHz mono = 320 samples.
type PCMFrame struct {
	Samples      []int16
	NewTalkspurt bool // true on first frame of a new TTS utterance
}

// TranscriptEvent carries an STT result through the pipeline.
type TranscriptEvent struct {
	Text      string
	Final     bool
	TurnStart time.Time // set on final transcripts for latency measurement
	// MergeWaitMs is how long the turn buffer held this turn open merging
	// continuations, so latency accounting can separate debounce from work.
	MergeWaitMs int64
	// OverAgentSpeech records whether the agent was mid-utterance when this
	// final landed. Captured at the STT callback rather than read later,
	// because the turn buffer holds a turn open for up to turnMergeMax and the
	// agent can finish speaking inside that window — by the time the agent
	// goroutine sees the turn, "was the caller talking over me?" is no longer
	// answerable. Turns merged from several finals keep the flag if ANY of
	// them landed over agent speech.
	OverAgentSpeech bool
}

// DataChannel message types sent to the client via the events DataChannel.

type transcriptMsg struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Final bool   `json:"final"`
}

type responseMsg struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type timingMsg struct {
	Type  string `json:"type"`
	Stage string `json:"stage"`
	Ms    int64  `json:"ms"`
}

type stateMsg struct {
	Type  string `json:"type"`
	State string `json:"state"`
}
