package udpmux

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ICE username fragments and passwords are parsed from an attacker-controlled
// STUN USERNAME and templated into the inferred client SDP offer, so they must
// be validated against the ice-char set and length bounds in RFC 8839 section
// 5.4 before use.
func TestICECredentialValidation(t *testing.T) {
	v2Ufrag := UfragPrefixV2 + strings.Repeat("a", 24)
	require.True(t, isICEUfrag("abcd"))                      // 4 chars, minimum ufrag
	require.True(t, isICEUfrag("libp2p+webrtc+v1/abcdEFGH")) // '+' and '/' are ice-char
	require.True(t, isICEUfrag(v2Ufrag))
	require.True(t, isICEPwd(strings.Repeat("a", 22))) // 22 chars, minimum password

	// charset violations (e.g. CRLF/SDP injection attempts)
	require.False(t, isICEUfrag("abc\r\na=candidate:x"))
	require.False(t, isICEUfrag("ab:cd")) // ':' is not an ice-char
	require.False(t, isICEPwd(strings.Repeat("a", 21)+"\n"))

	// length violations
	require.False(t, isICEUfrag("abc"))                    // 3 < 4
	require.False(t, isICEUfrag(""))                       // empty
	require.False(t, isICEPwd(strings.Repeat("a", 21)))    // 21 < 22
	require.False(t, isICEUfrag(strings.Repeat("a", 257))) // > 256

	// multi-byte UTF-8 input: the len() byte count differs from the character
	// count (and from the UTF-16 length js-libp2p measures), but both reject it
	// on the charset check, so the length-representation difference between the
	// implementations never changes the decision.
	require.False(t, isICEUfrag("abécd")) // 'é' is 2 UTF-8 bytes and not ice-char
	require.False(t, isICEUfrag("ab😀cd")) // emoji: 4 UTF-8 bytes / 2 UTF-16 units
}

// credentialsFromSTUNMessage runs before the mux allocates any state, so every
// malformed or unsupported STUN USERNAME must be rejected here rather than
// surfaced as a candidate.
func TestCredentialsFromSTUNMessage(t *testing.T) {
	v1 := UfragPrefixV1 + "abcdEFGH"
	v2Pwd := strings.Repeat("p", 24)
	v2 := UfragPrefixV2 + v2Pwd
	clientUfrag := "browserClientUfrag"

	for _, tc := range []struct {
		name        string
		username    string
		localUfrag  string
		remoteUfrag string
		remotePwd   string
		wantErr     bool
	}{
		{
			// v1 shares one value: the client password equals the client ufrag.
			name:        "valid v1",
			username:    v1 + ":" + v1,
			localUfrag:  v1,
			remoteUfrag: v1,
			remotePwd:   v1,
		},
		{
			// v2 recovers the client password from the server ufrag.
			name:        "valid v2",
			username:    v2 + ":" + clientUfrag,
			localUfrag:  v2,
			remoteUfrag: clientUfrag,
			remotePwd:   v2Pwd,
		},
		{name: "missing colon", username: v1, wantErr: true},
		{name: "empty local ufrag", username: ":" + clientUfrag, wantErr: true},
		{name: "empty remote ufrag", username: v1 + ":", wantErr: true},
		{name: "local ufrag too short", username: "ab:" + clientUfrag, wantErr: true},
		{name: "remote ufrag not ice-char", username: v1 + ":ab\r\ncd", wantErr: true},
		{name: "unknown version prefix", username: "abcdEFGH:abcdEFGH", wantErr: true},
		{name: "v2 recovered password too short", username: UfragPrefixV2 + "short:" + clientUfrag, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := getSTUNBindingRequestWithUsername(tc.username)
			local, remote, pwd, err := credentialsFromSTUNMessage(msg)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.localUfrag, local)
			require.Equal(t, tc.remoteUfrag, remote)
			require.Equal(t, tc.remotePwd, pwd)
		})
	}
}
