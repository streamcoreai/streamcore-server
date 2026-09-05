package plugin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ConfirmTokenField is the argument a gated tool must carry on its second call.
// It is stripped before the tool sees its parameters, so a tool's own schema
// never has to mention it.
const ConfirmTokenField = "confirm_token"

// DefaultConfirmationTTL bounds how long a spoken "yes" stays valid. Long
// enough for the agent to read the prompt and hear an answer, short enough
// that an abandoned turn cannot authorise a mutation minutes later.
const DefaultConfirmationTTL = 5 * time.Minute

// ConfirmationStore backs the two-call gate for tools that declare
// ConfirmationRequired. The first call mints a token and executes nothing; only
// a second call carrying that token runs.
//
// A token is bound to the session, the tool name, and a canonical hash of the
// arguments, so it cannot be replayed, moved to another caller, or reused after
// the model quietly changed a parameter between the question and the answer.
type ConfirmationStore struct {
	mu      sync.Mutex
	pending map[string]pendingConfirmation
	ttl     time.Duration
}

type pendingConfirmation struct {
	sessionID string
	tool      string
	argsHash  string
	expires   time.Time
}

// ConfirmationPrompter lets a tool describe the pending action in the words the
// agent should say out loud. Tools that do not implement it get a generic
// prompt naming the tool.
type ConfirmationPrompter interface {
	ConfirmationPrompt(params json.RawMessage) string
}

// NewConfirmationStore returns a store with the given token lifetime. A
// non-positive ttl uses DefaultConfirmationTTL.
func NewConfirmationStore(ttl time.Duration) *ConfirmationStore {
	if ttl <= 0 {
		ttl = DefaultConfirmationTTL
	}
	return &ConfirmationStore{pending: make(map[string]pendingConfirmation), ttl: ttl}
}

// ConfirmationChallenge is what the model gets back instead of a result when a
// gated tool is called without an approved token.
type ConfirmationChallenge struct {
	Status       string `json:"status"`
	ConfirmToken string `json:"confirm_token"`
	Prompt       string `json:"prompt"`
	Instructions string `json:"instructions"`
}

// Gate decides whether a call may run. It returns the arguments with any
// confirm_token removed, plus a challenge to return verbatim when the call is
// not yet authorised.
//
// Tools that do not require confirmation pass straight through, which is every
// tool that existed before this gate did.
func (s *ConfirmationStore) Gate(sessionID string, tool Tool, args json.RawMessage) (json.RawMessage, *ConfirmationChallenge, error) {
	token, clean, err := splitConfirmToken(args)
	if err != nil {
		return nil, nil, err
	}
	if !tool.ConfirmationRequired() {
		return clean, nil, nil
	}

	hash := hashArgs(clean)
	if token != "" {
		if err := s.consume(token, sessionID, tool.Name(), hash); err != nil {
			return nil, nil, err
		}
		return clean, nil, nil
	}

	prompt := fmt.Sprintf("Run %s?", tool.Name())
	if p, ok := tool.(ConfirmationPrompter); ok {
		if custom := p.ConfirmationPrompt(clean); custom != "" {
			prompt = custom
		}
	}

	return clean, &ConfirmationChallenge{
		Status:       "confirmation_required",
		ConfirmToken: s.issue(sessionID, tool.Name(), hash),
		Prompt:       prompt,
		Instructions: "Do not run this yet. Read the prompt to the user and wait for a spoken yes. " +
			"Only after they agree, call " + tool.Name() + " again with identical arguments plus " +
			ConfirmTokenField + " set to this value. If they decline, do not call it again.",
	}, nil
}

func (s *ConfirmationStore) issue(sessionID, tool, argsHash string) string {
	raw := make([]byte, 16)
	rand.Read(raw)
	token := "confirm_" + hex.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.pending[token] = pendingConfirmation{
		sessionID: sessionID,
		tool:      tool,
		argsHash:  argsHash,
		expires:   time.Now().Add(s.ttl),
	}
	return token
}

// consume validates and burns a token. Every failure reads the same to the
// model: an approval it does not hold cannot be distinguished from one that
// expired, so guessing gains nothing.
func (s *ConfirmationStore) consume(token, sessionID, tool, argsHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()

	entry, ok := s.pending[token]
	if !ok {
		return fmt.Errorf("that confirmation is not valid; ask the user again and use the new token")
	}
	delete(s.pending, token)

	if entry.sessionID != sessionID || entry.tool != tool {
		return fmt.Errorf("that confirmation is not valid; ask the user again and use the new token")
	}
	if entry.argsHash != argsHash {
		return fmt.Errorf("the arguments changed since the user confirmed; ask again with the new arguments")
	}
	return nil
}

func (s *ConfirmationStore) sweepLocked() {
	now := time.Now()
	for token, entry := range s.pending {
		if now.After(entry.expires) {
			delete(s.pending, token)
		}
	}
}

// splitConfirmToken lifts confirm_token out of a tool's JSON arguments. Empty
// or absent arguments are normalised to an empty object so callers never have
// to special-case them.
func splitConfirmToken(args json.RawMessage) (string, json.RawMessage, error) {
	if len(args) == 0 {
		return "", json.RawMessage(`{}`), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		// Not an object — no token to lift, and the tool can reject it itself.
		return "", args, nil
	}
	rawToken, ok := fields[ConfirmTokenField]
	if !ok {
		return "", args, nil
	}
	delete(fields, ConfirmTokenField)

	var token string
	if err := json.Unmarshal(rawToken, &token); err != nil {
		return "", nil, fmt.Errorf("%s must be a string", ConfirmTokenField)
	}
	clean, err := json.Marshal(fields)
	if err != nil {
		return "", nil, err
	}
	return token, clean, nil
}

// hashArgs canonicalises arguments before hashing so that key order and
// whitespace cannot make an identical call look like a different one. Go
// marshals maps with sorted keys, which is the whole trick.
func hashArgs(args json.RawMessage) string {
	canonical := args
	var value any
	if json.Unmarshal(args, &value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			canonical = encoded
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
