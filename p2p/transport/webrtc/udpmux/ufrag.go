package udpmux

// The "libp2p+webrtc+" namespace selects the WebRTC Direct handshake version via
// the ICE username fragment. v1 ("libp2p+webrtc+v1/") munges the SDP; v2
// ("libp2p+webrtc+v2/") does not, because Chromium's WebRTC-NoSdpMangleUfrag
// field trial rejects an offer whose ICE ufrag was munged. A v2 client instead
// leaves its local credentials untouched and puts its own ICE password in the
// server ufrag as "libp2p+webrtc+v2/<client_pwd>", which the server reads back
// from the STUN USERNAME with no SDP exchange. A server MUST reject a version it
// does not recognize rather than guess one. v2 was introduced in
// https://github.com/libp2p/specs/pull/715; the spec lives at
// https://github.com/libp2p/specs/blob/master/webrtc/webrtc-direct.md and the
// js-libp2p implementation at https://github.com/libp2p/js-libp2p/pull/3480.
const (
	UfragPrefixV1 = "libp2p+webrtc+v1/"
	UfragPrefixV2 = "libp2p+webrtc+v2/"
)

const (
	// ICE credential bounds, per RFC 8839 section 5.4
	// (ufrag = 4*256ice-char, password = 22*256ice-char).
	iceUfragMinLen   = 4
	icePwdMinLen     = 22
	iceCredentialMax = 256
)

// ice-char is ASCII-only, so any string that passes isICECharString has exactly
// one byte per character. The len() byte count used by isICEUfrag/isICEPwd then
// equals both the ice-char count and the UTF-16 length that js-libp2p measures,
// so the two implementations accept the same credentials; non-ice-char input
// (including any multi-byte UTF-8) is rejected by both regardless of how each
// counts length.

// isICEChar reports whether b is in the RFC 8839 ice-char set
// (ALPHA / DIGIT / "+" / "/").
func isICEChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '+' || b == '/'
}

func isICECharString(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isICEChar(s[i]) {
			return false
		}
	}
	return true
}

// isICEUfrag reports whether s is a syntactically valid ICE username fragment
// (RFC 8839 section 5.4: ufrag = 4*256ice-char).
func isICEUfrag(s string) bool {
	return len(s) >= iceUfragMinLen && len(s) <= iceCredentialMax && isICECharString(s)
}

// isICEPwd reports whether s is a syntactically valid ICE password
// (RFC 8839 section 5.4: password = 22*256ice-char).
func isICEPwd(s string) bool {
	return len(s) >= icePwdMinLen && len(s) <= iceCredentialMax && isICECharString(s)
}
