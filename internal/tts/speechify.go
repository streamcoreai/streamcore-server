package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sync"
)

const (
	speechifyAPIURL = "https://api.speechify.ai/v1/audio/speech"
	// Geffen — Simba 3.2 curated voice (Speechify docs example)
	defaultSpeechifyVoiceID = "geffen_32"
	defaultSpeechifyModel   = "simba-3.2"
)

type speechifyClient struct {
	apiKey     string
	voiceID    string
	model      string
	httpClient *http.Client
}

// NewSpeechifyClient creates a Speechify Simba TTS client.
// voiceID defaults to Geffen if empty. model defaults to simba-3.2.
func NewSpeechifyClient(apiKey, voiceID, model string) Client {
	if voiceID == "" {
		voiceID = defaultSpeechifyVoiceID
	}
	if model == "" {
		model = defaultSpeechifyModel
	}
	return &speechifyClient{
		apiKey:     apiKey,
		voiceID:    voiceID,
		model:      model,
		httpClient: newPooledHTTPClient(),
	}
}

type speechifyRequest struct {
	Input        string `json:"input"`
	VoiceID      string `json:"voice_id"`
	Model        string `json:"model"`
	AudioFormat  string `json:"audio_format"`
	OutputFormat string `json:"output_format"`
}

// speechifyResponse — unlike Cartesia/ElevenLabs, Speechify returns the audio
// base64-encoded inside a JSON envelope rather than as raw bytes.
type speechifyResponse struct {
	AudioData   string `json:"audio_data"`
	AudioFormat string `json:"audio_format"`
}

func (c *speechifyClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	// The API silently ignores pcm_16000 for the simba-3.x models and returns
	// their native 24kHz instead (only legacy simba-english honors it), so
	// request pcm_24000 — honored by every model — and downsample to 16kHz.
	body := speechifyRequest{
		Input:        text,
		VoiceID:      c.voiceID,
		Model:        c.model,
		AudioFormat:  "pcm",
		OutputFormat: "pcm_24000",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("speechify marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, speechifyAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("speechify create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("speechify request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("speechify error %d: %s", resp.StatusCode, string(respBody))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("speechify read response: %w", err)
	}

	var parsed speechifyResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("speechify parse response: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(parsed.AudioData)
	if err != nil {
		return nil, fmt.Errorf("speechify decode audio: %w", err)
	}

	pcm := resample24kTo16k(data)

	log.Printf("[tts:speechify] synthesized %d bytes for %d chars of text", len(pcm), len(text))
	return pcm, nil
}

// 24 kHz → 16 kHz is a 2/3 rational conversion, which needs a low-pass filter
// before decimation. Simply keeping two samples out of every three does not:
// source content above the 8 kHz output Nyquist folds straight back into the
// audible band (measured on the previous implementation, a 9 kHz tone survived
// at −3 dB and reappeared at 7 kHz), and because the two output phases were
// filtered differently it also produced an image at |f − 8 kHz| for perfectly
// in-band content (a 7 kHz tone generated a 1 kHz spur only 12 dB down).
// Sibilants — /s/, /ʃ/ and /t/ bursts, which carry much of their energy at
// 5–11 kHz — were affected worst.
//
// The standard construction below upsamples ×2, low-passes, and decimates ×3.
// Because only every second sample of the upsampled stream is non-zero, the
// zero taps are skipped and roughly half the kernel runs per output sample.
// Measured stopband rejection is better than 79 dB across 8–12 kHz.
const (
	resampleSourceRate = 24000
	resampleUp         = 2
	resampleDown       = 3
	// resampleTaps is odd so the group delay (2 ms here) is a whole number of
	// samples at the intermediate rate and the output stays time-aligned.
	resampleTaps = 193
	// resampleCutoff leaves an 800 Hz transition band below the 8 kHz output
	// Nyquist. Speech intelligibility carries almost nothing above this.
	resampleCutoff = 7200.0
	// resampleBeta is the Kaiser shape parameter; 8.0 gives ~80 dB stopband.
	resampleBeta = 8.0
)

var (
	resampleFIROnce sync.Once
	resampleFIR     []float64
)

// resampleKernel builds the Kaiser-windowed sinc once and caches it. The
// kernel is designed at the upsampled (48 kHz) rate, and its DC gain is
// normalised to resampleUp to cancel the loss from zero-stuffing.
func resampleKernel() []float64 {
	resampleFIROnce.Do(func() {
		h := make([]float64, resampleTaps)
		mid := float64(resampleTaps-1) / 2
		fc := resampleCutoff / float64(resampleSourceRate*resampleUp)
		i0beta := besselI0(resampleBeta)

		var sum float64
		for i := range h {
			t := float64(i) - mid
			sinc := 2 * fc
			if t != 0 {
				sinc = math.Sin(2*math.Pi*fc*t) / (math.Pi * t)
			}
			r := 2*float64(i)/float64(resampleTaps-1) - 1
			window := besselI0(resampleBeta*math.Sqrt(math.Max(0, 1-r*r))) / i0beta
			h[i] = sinc * window
			sum += h[i]
		}
		for i := range h {
			h[i] *= float64(resampleUp) / sum
		}
		resampleFIR = h
	})
	return resampleFIR
}

// besselI0 is the zeroth-order modified Bessel function of the first kind,
// evaluated by its power series. It converges in a few dozen terms for the
// beta values used in window design.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 64; k++ {
		q := x / (2 * float64(k))
		term *= q * q
		sum += term
		if term < 1e-16*sum {
			break
		}
	}
	return sum
}

// resample24kTo16k converts s16le mono PCM from 24 kHz to 16 kHz.
func resample24kTo16k(in []byte) []byte {
	n := len(in) / 2
	if n == 0 {
		return nil
	}

	x := make([]float64, n)
	for i := 0; i < n; i++ {
		x[i] = float64(int16(uint16(in[2*i]) | uint16(in[2*i+1])<<8))
	}

	h := resampleKernel()
	half := (len(h) - 1) / 2
	outN := (n*resampleUp + resampleDown - 1) / resampleDown
	out := make([]byte, 0, outN*2)

	for m := 0; m < outN; m++ {
		// Position in the virtual upsampled stream, shifted by the filter's
		// group delay so the output is not offset in time.
		center := m*resampleDown + half

		lo := (center - len(h) + 1) / resampleUp
		if lo < 0 {
			lo = 0
		}
		hi := center / resampleUp
		if hi > n-1 {
			hi = n - 1
		}

		var acc float64
		for i := lo; i <= hi; i++ {
			// Sample i of the input sits at index i*resampleUp of the
			// upsampled stream; every other index there is a zero we skip.
			if k := center - i*resampleUp; k >= 0 && k < len(h) {
				acc += x[i] * h[k]
			}
		}

		v := math.Round(acc)
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		s := uint16(int16(v))
		out = append(out, byte(s), byte(s>>8))
	}
	return out
}

// SynthesizeStream emits the whole utterance as one chunk. Speechify returns
// base64 audio inside a JSON envelope, and the 24 kHz to 16 kHz resampler
// needs a complete buffer, so there is nothing to stream incrementally.
func (c *speechifyClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return streamWhole(ctx, c.Synthesize, text)
}
