package peer

import (
	"context"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

// A WHIP answer has to carry the server's candidates, so HandleOffer waits for
// ICE gathering. The host candidates are ready in a couple of milliseconds;
// the server-reflexive one needs a round trip to the STUN server, which from a
// distant region (Google's from New Zealand: ~550ms) is most of call setup.
// A caller on the same machine or LAN reaches a host candidate and never uses
// the reflexive one, so for those callers the answer goes out without it.
// Anyone else still gets the full wait.

// DefaultSTUNURL is the STUN server used when the built-in TURN server is not
// configured.
const DefaultSTUNURL = "stun:stun.l.google.com:19302"

// localAnswerWait is how long a local caller's answer waits for gathering.
// Host candidates are in within a few milliseconds; this leaves room for a
// slow interface enumeration without adding a noticeable wait.
const localAnswerWait = 25 * time.Millisecond

// reflexiveIP is this server's public address as a STUN server last reported
// it. A caller whose own reflexive address matches sits behind the same NAT.
var reflexiveIP atomic.Value // string

func noteReflexive(c *webrtc.ICECandidate) {
	if c != nil && c.Typ == webrtc.ICECandidateTypeSrflx && c.Address != "" {
		reflexiveIP.Store(c.Address)
	}
}

// WarmReflexiveAddress learns the server's public address in the background, so
// the first LAN caller after startup is recognised too. Best effort: on
// failure callers simply get the full gathering wait.
func WarmReflexiveAddress(stunURL string) {
	go func() {
		pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{stunURL}}}})
		if err != nil {
			return
		}
		defer func() { _ = pc.Close() }()
		pc.OnICECandidate(noteReflexive)
		if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
			return
		}
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			return
		}
		done := webrtc.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(offer); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		select {
		case <-done:
		case <-ctx.Done():
		}
	}()
}

// callerIsLocal reports whether the offer shows the caller can reach one of
// this server's host candidates: a loopback or same-subnet host candidate, or
// a reflexive address equal to the server's own. Browsers hide host addresses
// behind mDNS names, so for them only the reflexive match can apply. An offer
// with no candidates (a trickle client) is not local.
func callerIsLocal(sdp string) bool {
	nets := localNetworks()
	ours, _ := reflexiveIP.Load().(string)
	// Unrelated networks often share a private range (192.168.1.0/24), so a
	// host candidate in our subnet only counts when the caller has no
	// reflexive address of its own that says it is somewhere else.
	sameSubnet, foreignReflexive := false, false
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		f := strings.Fields(line)
		// a=candidate:<foundation> <component> <transport> <priority> <address> <port> typ <type> ...
		if len(f) < 8 || f[6] != "typ" {
			continue
		}
		addr, typ := f[4], f[7]
		switch typ {
		case "host":
			ip := net.ParseIP(addr)
			if ip == nil {
				continue // mDNS name
			}
			if ip.IsLoopback() {
				return true
			}
			for _, n := range nets {
				if n.Contains(ip) {
					sameSubnet = true
				}
			}
		case "srflx":
			if ours != "" && addr == ours {
				return true
			}
			foreignReflexive = true
		}
	}
	return sameSubnet && !foreignReflexive
}

func localNetworks() []*net.IPNet {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n)
		}
	}
	return out
}

// gatherWait is how long HandleOffer waits for ICE gathering before answering.
// Only peers on the public STUN server take the shortcut: with the built-in
// STUN/TURN server gathering is fast anyway, and the relay candidate a
// firewalled caller depends on must not be cut off.
func gatherWait(id, sdp string, publicSTUN bool) time.Duration {
	if publicSTUN && callerIsLocal(sdp) {
		log.Printf("[peer:%s] caller is local, answering with host candidates", id)
		return localAnswerWait
	}
	return 5 * time.Second
}
