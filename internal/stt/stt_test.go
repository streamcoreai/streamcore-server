package stt

import "testing"

// noopClient is a Client that carries no opinion about partials. It is the
// shape of every provider that predates the capability flag.
type noopClient struct{}

func (noopClient) SendAudio([]byte) error { return nil }
func (noopClient) Close()                 {}

// A provider that does not implement PartialsEmitter must not satisfy it:
// runInbound's assertion defaults to true, which is what keeps every
// existing provider on the partials-driven path. If a future edit makes
// the base Client interface carry EmitsPartials (or the method moves to a
// type every client embeds), this test fails before that default silently
// flips for providers that never opted in.
func TestPlainClientDoesNotSatisfyPartialsEmitter(t *testing.T) {
	var c Client = noopClient{}
	if _, ok := c.(PartialsEmitter); ok {
		t.Error("plain Client satisfies PartialsEmitter, want absence to keep the partials default")
	}
}
