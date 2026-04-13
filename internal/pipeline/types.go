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

