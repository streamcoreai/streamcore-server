package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const (
	whisperModel   = "whisper-1"
	sampleRate     = 16000
	numChannels    = 1
	bitsPerSample  = 16
	bytesPerSample = bitsPerSample / 8

	// VAD parameters
	speechEnergyThreshold = 500.0                  // RMS threshold to detect speech
	silenceTimeout        = 600 * time.Millisecond // Silence after speech triggers transcription
	maxBufferDuration     = 30 * time.Second       // Force-flush after 30s
	minSpeechDuration     = 200 * time.Millisecond // Ignore very short bursts
)

// openaiClient implements STT using the OpenAI Whisper API.
// Since Whisper is a batch API, audio is buffered and sent for transcription
// when silence is detected after speech (simple energy-based VAD).
// Unlike Deepgram, only final transcripts are produced (no partials).
type openaiClient struct {
	client   *openai.Client
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	onResult func(TranscriptResult)

	// Audio buffer (linear16 PCM, 16kHz mono)
	audioBuf bytes.Buffer

	// VAD state
	speaking    bool
	speechStart time.Time
	lastSpeech  time.Time
	vadTicker   *time.Ticker
	done        chan struct{}
}

func NewOpenAIClient(ctx context.Context, apiKey string, onResult func(TranscriptResult)) (*openaiClient, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	c := &openaiClient{
		client:   openai.NewClient(apiKey),
		ctx:      sttCtx,
		cancel:   cancel,
		onResult: onResult,
		done:     make(chan struct{}),
	}

	c.vadTicker = time.NewTicker(100 * time.Millisecond)
	go c.vadLoop()

	log.Println("[stt] OpenAI Whisper client ready")
	return c, nil
}

// vadLoop periodically checks if we should flush buffered speech to Whisper.
func (c *openaiClient) vadLoop() {
	defer close(c.done)
	for {
		select {
		case <-c.ctx.Done():
			// Flush any remaining audio on shutdown
			c.flushIfNeeded(true)
			return
		case <-c.vadTicker.C:
			c.flushIfNeeded(false)
		}
	}
}

// flushIfNeeded checks if speech has ended (silence after speech) and sends
// the buffered audio to Whisper for transcription.
func (c *openaiClient) flushIfNeeded(force bool) {
	c.mu.Lock()

	if c.audioBuf.Len() == 0 || !c.speaking {
		c.mu.Unlock()
		return
	}

	silentFor := time.Since(c.lastSpeech)
	speechDur := time.Since(c.speechStart)
	bufDur := time.Duration(c.audioBuf.Len()/bytesPerSample/sampleRate) * time.Second

	shouldFlush := false
	if force {
		shouldFlush = true
	} else if silentFor > silenceTimeout && speechDur > minSpeechDuration {
		shouldFlush = true
	} else if bufDur >= maxBufferDuration {
		shouldFlush = true
	}

	if !shouldFlush {
		c.mu.Unlock()
		return
	}

	// Take the buffer and reset state
	pcmData := make([]byte, c.audioBuf.Len())
	copy(pcmData, c.audioBuf.Bytes())
	c.audioBuf.Reset()
	c.speaking = false
	c.mu.Unlock()

	// Transcribe in background so we don't block the VAD loop
	go c.transcribe(pcmData)
}

// SendAudio receives raw linear16 PCM bytes (16kHz mono) and buffers them.
func (c *openaiClient) SendAudio(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ctx.Err() != nil {
		return fmt.Errorf("client closed")
	}

	c.audioBuf.Write(data)

	// Calculate RMS energy for VAD
	energy := rmsEnergy(data)
	if energy > speechEnergyThreshold {
		if !c.speaking {
			c.speaking = true
			c.speechStart = time.Now()
			log.Println("[stt:whisper] speech started")
		}
		c.lastSpeech = time.Now()
	}

	return nil
}

// transcribe encodes PCM as WAV and sends it to the Whisper API.
func (c *openaiClient) transcribe(pcmData []byte) {
	if c.ctx.Err() != nil {
		return
	}

	wavData := encodeWAV(pcmData, sampleRate, numChannels, bitsPerSample)

	resp, err := c.client.CreateTranscription(c.ctx, openai.AudioRequest{
		Model:    whisperModel,
		Reader:   bytes.NewReader(wavData),
		FilePath: "audio.wav",
	})
	if err != nil {
		if c.ctx.Err() == nil {
			log.Printf("[stt:whisper] transcription error: %v", err)
		}
		return
	}

	text := resp.Text
	if text == "" {
		return
	}

	log.Printf("[stt:whisper] transcript: %s", text)
	c.onResult(TranscriptResult{
		Text:    text,
		IsFinal: true,
	})
}

func (c *openaiClient) Close() {
	c.cancel()
	c.vadTicker.Stop()
	<-c.done // Wait for vadLoop to finish
	log.Println("[stt:whisper] closed")
}

// rmsEnergy calculates the root-mean-square energy of linear16 PCM samples.
func rmsEnergy(data []byte) float64 {
	if len(data) < 2 {
		return 0
	}
	reader := bytes.NewReader(data)
	var sumSquares float64
	count := 0
	for {
		var sample int16
		if err := binary.Read(reader, binary.LittleEndian, &sample); err != nil {
			break
		}
		sumSquares += float64(sample) * float64(sample)
		count++
	}
	if count == 0 {
		return 0
	}
	return math.Sqrt(sumSquares / float64(count))
}

// encodeWAV wraps raw PCM data in a WAV header.
func encodeWAV(pcm []byte, sampleRate, channels, bitsPerSample int) []byte {
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	dataSize := len(pcm)

	buf := &bytes.Buffer{}
	// RIFF header
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	// fmt chunk
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, uint32(16))            // chunk size
	binary.Write(buf, binary.LittleEndian, uint16(1))             // PCM format
	binary.Write(buf, binary.LittleEndian, uint16(channels))      // channels
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate))    // sample rate
	binary.Write(buf, binary.LittleEndian, uint32(byteRate))      // byte rate
	binary.Write(buf, binary.LittleEndian, uint16(blockAlign))    // block align
	binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample)) // bits per sample
	// data chunk
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(pcm)

	return buf.Bytes()
}
