package main

import (
	"encoding/json"
	"log"
	"mime"
	"net/http"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// Build metadata, stamped at link time:
//
//	go build -ldflags "-X main.version=v0.1.0 -X main.commit=$(git rev-parse HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// The names are goreleaser's defaults, so a release workflow needs no ldflags
// of its own. Anything left empty falls back to what the Go toolchain recorded
// in the binary; see resolveBuildInfo.
var (
	version string
	commit  string
	date    string
)

// buildInfo is the body of GET /version.
type buildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	Go      string `json:"go"`
}

// currentBuildInfo resolves the running binary's metadata once; it cannot
// change while the process lives.
var currentBuildInfo = sync.OnceValue(func() buildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return resolveBuildInfo(version, commit, date, bi)
})

// resolveBuildInfo prefers the link-time values and falls back to the build
// info the toolchain embeds, so a plain `go build` or `go install` still says
// which code it is:
//
//   - version: the main module version — a tag for `go install ...@v0.1.0`, a
//     pseudo-version for `go build` in a git checkout (Go 1.24+) — else "dev".
//   - commit: vcs.revision, with "-dirty" when the tree had local changes,
//     else "unknown".
//   - built: vcs.time, which is the commit's time and so a lower bound on the
//     build time, else "unknown".
//   - go: the toolchain that built the binary.
//
// A Docker build has no .git in its context, which is why the image takes the
// link-time values as build args instead.
func resolveBuildInfo(version, commit, date string, bi *debug.BuildInfo) buildInfo {
	info := buildInfo{Version: version, Commit: commit, Built: date, Go: runtime.Version()}
	if bi != nil {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		var revision, vcsTime string
		var modified bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.time":
				vcsTime = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if info.Commit == "" && revision != "" {
			info.Commit = revision
			if modified {
				info.Commit += "-dirty"
			}
		}
		if info.Built == "" {
			info.Built = vcsTime
		}
		if bi.GoVersion != "" {
			info.Go = bi.GoVersion
		}
	}
	setDefault := func(field *string, fallback string) {
		if *field == "" {
			*field = fallback
		}
	}
	setDefault(&info.Version, "dev")
	setDefault(&info.Commit, "unknown")
	setDefault(&info.Built, "unknown")
	return info
}

// versionHandler serves GET /version. The body is fixed for the life of the
// process, so it is encoded once.
//
// no-store because the point of the endpoint is to confirm what a rollout put
// live; a cached answer from before the deploy would defeat it.
func versionHandler(info buildInfo) http.HandlerFunc {
	body, err := json.Marshal(info)
	if err != nil {
		// Four string fields cannot fail to marshal.
		panic(err)
	}
	body = append(body, '\n')
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if _, err := w.Write(body); err != nil {
			log.Printf("version: write response: %v", err)
		}
	}
}

// providerNames is which providers the server was configured with — names
// only. Nothing here may come from a credential: /health is unauthenticated
// and reachable from the internet on a default deployment.
type providerNames struct {
	STT      string `json:"stt,omitempty"`
	LLM      string `json:"llm,omitempty"`
	TTS      string `json:"tts,omitempty"`
	Realtime string `json:"realtime,omitempty"`
}

// The provider names each package's constructor accepts. A configured value is
// only echoed when it is one of these: config.Load does not reject an unknown
// classic provider, so the raw field can hold anything — a pasted URL, or a key
// in the wrong slot — and /health must not repeat it to the internet.
// TestProviderAllowlistsMatchTheConstructors keeps these in step with the
// constructors' switch statements.
var (
	sttProviders      = nameSet("aliyun", "assemblyai", "deepgram", "moonshine", "openai", "telnyx", "vibevoice", "volcengine")
	llmProviders      = nameSet("agent", "ollama", "openai")
	ttsProviders      = nameSet("aliyun", "cartesia", "deepgram", "elevenlabs", "mimo", "minimax", "moonshine", "openai", "speechify", "telnyx", "vibevoice", "volcengine")
	realtimeProviders = nameSet("grok")
)

func nameSet(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// knownProvider returns name when the server recognises it, "unknown" for any
// other non-empty value, and "" (omitted from the JSON) when unset.
func knownProvider(known map[string]bool, name string) string {
	switch {
	case name == "":
		return ""
	case known[name]:
		return name
	default:
		return "unknown"
	}
}

// configuredProviders mirrors the startup log line: in speech-to-speech mode
// no STT, LLM or TTS provider is constructed, so naming them would mislead.
func configuredProviders(cfg *config.Config) providerNames {
	if cfg.RealtimeEnabled() {
		return providerNames{Realtime: knownProvider(realtimeProviders, cfg.Realtime.Provider)}
	}
	return providerNames{
		STT: knownProvider(sttProviders, cfg.STT.Provider),
		LLM: knownProvider(llmProviders, cfg.LLM.Provider),
		TTS: knownProvider(ttsProviders, cfg.TTS.Provider),
	}
}

// healthReport is the verbose body of GET /health.
type healthReport struct {
	Status         string        `json:"status"`
	Uptime         string        `json:"uptime"`
	UptimeSeconds  int64         `json:"uptime_seconds"`
	ActiveSessions int           `json:"active_sessions"`
	Providers      providerNames `json:"providers"`
}

// healthHandler serves GET /health.
//
// By default it answers exactly what it always has: 200 and the plain body
// "ok", for load balancers, existing health checks (the deploy workflow's
// curl -sf checks the status) and any caller that expects the "ok" body. A
// caller that asks for application/json by name gets a healthReport instead.
//
// It reports only what the process already knows. Upstream providers are never
// probed: that would turn a liveness check into a dependency check, and a
// vendor blip would take the service out of rotation.
type healthHandler struct {
	started   time.Time
	now       func() time.Time
	sessions  func() int
	providers providerNames
}

// newHealthHandler builds the /health handler for a loaded config. sessions
// reports the live session count, read on every verbose request.
func newHealthHandler(cfg *config.Config, started time.Time, sessions func() int) *healthHandler {
	return &healthHandler{
		started:   started,
		now:       time.Now,
		sessions:  sessions,
		providers: configuredProviders(cfg),
	}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The body depends on Accept, so a shared cache must key on it or it can
	// hand the JSON form to a checker that expects "ok".
	w.Header().Add("Vary", "Accept")
	if !wantsJSON(r.Header.Values("Accept")) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			log.Printf("health: write response: %v", err)
		}
		return
	}

	uptime := h.now().Sub(h.started).Truncate(time.Second)
	if uptime < 0 {
		uptime = 0
	}
	report := healthReport{
		Status:         "ok",
		Uptime:         uptime.String(),
		UptimeSeconds:  int64(uptime / time.Second),
		ActiveSessions: h.sessions(),
		Providers:      h.providers,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(report); err != nil {
		log.Printf("health: write response: %v", err)
	}
}

// wantsJSON reports whether an Accept header prefers application/json over the
// default text/plain, by RFC 9110 §12.5.1.
//
// JSON has to be asked for by name: wildcards such as */* — what curl, fetch()
// and most health checkers send — never select it. Each body takes its q from
// the most specific range that matches it, a range naming charset=utf-8 being
// more specific than the same range without one:
//
//	JSON:  application/json;charset=utf-8, application/json
//	plain: text/plain;charset=utf-8, text/plain, text/*;charset=utf-8,
//	       text/*, */*;charset=utf-8, */*
//
// with the highest q among ranges of the same specificity. JSON is served when
// its q is non-zero and strictly higher than plain's. A tie keeps the default,
// so a client listing both, like axios's "application/json, text/plain, */*",
// still gets "ok".
//
// Ranges are split on commas outside quoted strings, so a comma inside a
// quoted parameter never starts a new range, and each is parsed with
// mime.ParseMediaType. That applies MIME parameter decoding — quoted values
// are unquoted (q="0.9" is 0.9) and RFC 2231 extended parameters such as q*
// and charset* in the us-ascii or utf-8 encoding are decoded into the q and
// charset keys (one in any other encoding is dropped) — before anything here
// reads them. A range is skipped when it does not parse, when its resolved q
// is not a qvalue (RFC 9110 §12.4.2: "0" or "1" with at most three decimals),
// or when its resolved charset is not utf-8, since both bodies are UTF-8.
// Parameters other than the resolved q and charset are ignored.
func wantsJSON(accept []string) bool {
	type rangeKey struct {
		mediaType string
		utf8      bool // the range names charset=utf-8
	}
	best := map[rangeKey]float64{}
	for _, header := range accept {
		for _, part := range splitHeaderList(header) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			mediaType, params, err := mime.ParseMediaType(part)
			if err != nil {
				continue
			}
			q := 1.0
			if raw, ok := params["q"]; ok {
				if q, ok = parseQValue(raw); !ok {
					continue
				}
			}
			charset, hasCharset := params["charset"]
			if hasCharset && !strings.EqualFold(charset, "utf-8") {
				continue
			}
			switch mediaType {
			case "application/json", "text/plain", "text/*", "*/*":
				key := rangeKey{mediaType, hasCharset}
				best[key] = max(best[key], q)
			}
		}
	}
	// mostSpecific returns the q of the first of keys present in best.
	mostSpecific := func(keys ...rangeKey) (float64, bool) {
		for _, key := range keys {
			if q, ok := best[key]; ok {
				return q, true
			}
		}
		return 0, false
	}
	jsonQ, _ := mostSpecific(
		rangeKey{"application/json", true}, rangeKey{"application/json", false})
	if jsonQ == 0 {
		return false
	}
	plainQ, ok := mostSpecific(
		rangeKey{"text/plain", true}, rangeKey{"text/plain", false},
		rangeKey{"text/*", true}, rangeKey{"text/*", false},
		rangeKey{"*/*", true}, rangeKey{"*/*", false})
	if !ok {
		return true
	}
	return jsonQ > plainQ
}

// splitHeaderList splits a comma-separated header value on the commas outside
// quoted strings, honouring backslash escapes inside them. An unterminated
// quoted string makes the element it starts malformed, so it is dropped.
func splitHeaderList(value string) []string {
	var elements []string
	start, quoted, escaped := 0, false, false
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			elements = append(elements, value[start:i])
			start = i + 1
		}
	}
	if quoted {
		return elements
	}
	return append(elements, value[start:])
}

// parseQValue parses an RFC 9110 §12.4.2 qvalue:
//
//	qvalue = ( "0" [ "." 0*3DIGIT ] ) / ( "1" [ "." 0*3("0") ] )
//
// Anything else — signs, exponents, a leading dot, a fourth decimal — is
// malformed.
func parseQValue(raw string) (float64, bool) {
	if raw == "" || len(raw) > 5 || (raw[0] != '0' && raw[0] != '1') {
		return 0, false
	}
	if len(raw) > 1 {
		if raw[1] != '.' {
			return 0, false
		}
		for _, c := range raw[2:] {
			if c < '0' || c > '9' || (raw[0] == '1' && c != '0') {
				return 0, false
			}
		}
	}
	q, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return q, true
}
