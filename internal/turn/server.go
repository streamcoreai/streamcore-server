package turn

import (
	"fmt"
	"log"
	"net"

	"github.com/pion/turn/v4"
)

// Server wraps a Pion TURN server that also handles STUN binding requests.
// It listens on UDP and TCP port 3478 (the standard STUN/TURN port) and relays
// media on UDP ports 50001-60000. This replaces the external coturn container.
type Server struct {
	server *turn.Server
}

// Start creates and starts an embedded STUN/TURN server.
//
//   - publicIP: the server's external IP advertised to clients (e.g., the EC2
//     Elastic IP). Used as both the relay address and the realm.
//   - secret:   the shared password for the long-term credential user
//     "voiceagent". Clients authenticate with username "voiceagent" and this
//     password.
func Start(publicIP, secret string) (*Server, error) {
	udpListener, err := net.ListenPacket("udp4", "0.0.0.0:3478")
	if err != nil {
		return nil, fmt.Errorf("turn: listen UDP :3478: %w", err)
	}

	// Also listen on TCP so clients behind firewalls that block outbound UDP
	// can still reach the relay.
	tcpListener, err := net.Listen("tcp4", "0.0.0.0:3478")
	if err != nil {
		udpListener.Close()
		return nil, fmt.Errorf("turn: listen TCP :3478: %w", err)
	}

	realm := publicIP

	s, err := turn.NewServer(turn.ServerConfig{
		Realm: realm,
		AuthHandler: func(username, realm string, srcAddr net.Addr) ([]byte, bool) {
			if username == "voiceagent" {
				return turn.GenerateAuthKey(username, realm, secret), true
			}
			return nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: udpListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
					RelayAddress: net.ParseIP(publicIP),
					Address:      "0.0.0.0",
					MinPort:      50001,
					MaxPort:      60000,
				},
			},
		},
		ListenerConfigs: []turn.ListenerConfig{
			{
				Listener: tcpListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
					RelayAddress: net.ParseIP(publicIP),
					Address:      "0.0.0.0",
					MinPort:      50001,
					MaxPort:      60000,
				},
			},
		},
	})
	if err != nil {
		udpListener.Close()
		tcpListener.Close()
		return nil, fmt.Errorf("turn: start server: %w", err)
	}

	log.Printf("Built-in TURN/STUN server listening on :3478 UDP+TCP (relay %s:50001-60000)", publicIP)
	return &Server{server: s}, nil
}

// Close shuts down the TURN server and releases all relay allocations.
func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Close()
}
