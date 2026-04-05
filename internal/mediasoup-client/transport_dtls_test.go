package mediasoupclient

import "testing"

func TestExtractDtlsParametersFromSDPSessionLevel(t *testing.T) {
	rawSDP := "v=0\r\n" +
		"o=- 1000 2 IN IP4 0.0.0.0\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"a=setup:actpass\r\n" +
		"a=fingerprint:sha-256 AA:BB:CC:DD:EE:FF\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:0\r\n"

	dtlsParameters, err := extractDtlsParametersFromSDP(rawSDP)
	if err != nil {
		t.Fatalf("extractDtlsParametersFromSDP() failed: %v", err)
	}

	if dtlsParameters.Role != DtlsRoleAuto {
		t.Fatalf("expected role %q, got %q", DtlsRoleAuto, dtlsParameters.Role)
	}

	if len(dtlsParameters.Fingerprints) != 1 {
		t.Fatalf("expected one fingerprint, got %d", len(dtlsParameters.Fingerprints))
	}

	fingerprint := dtlsParameters.Fingerprints[0]
	if fingerprint.Algorithm != "sha-256" {
		t.Fatalf("expected algorithm %q, got %q", "sha-256", fingerprint.Algorithm)
	}
	if fingerprint.Value != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("expected fingerprint value %q, got %q", "AA:BB:CC:DD:EE:FF", fingerprint.Value)
	}
}

func TestExtractDtlsParametersFromSDPMediaLevelFallback(t *testing.T) {
	rawSDP := "v=0\r\n" +
		"o=- 1000 2 IN IP4 0.0.0.0\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:datachannel\r\n" +
		"a=setup:active\r\n" +
		"a=fingerprint:sha-384 11:22:33:44\r\n"

	dtlsParameters, err := extractDtlsParametersFromSDP(rawSDP)
	if err != nil {
		t.Fatalf("extractDtlsParametersFromSDP() failed: %v", err)
	}

	if dtlsParameters.Role != DtlsRoleClient {
		t.Fatalf("expected role %q, got %q", DtlsRoleClient, dtlsParameters.Role)
	}

	if len(dtlsParameters.Fingerprints) != 1 {
		t.Fatalf("expected one fingerprint, got %d", len(dtlsParameters.Fingerprints))
	}

	fingerprint := dtlsParameters.Fingerprints[0]
	if fingerprint.Algorithm != "sha-384" {
		t.Fatalf("expected algorithm %q, got %q", "sha-384", fingerprint.Algorithm)
	}
	if fingerprint.Value != "11:22:33:44" {
		t.Fatalf("expected fingerprint value %q, got %q", "11:22:33:44", fingerprint.Value)
	}
}
