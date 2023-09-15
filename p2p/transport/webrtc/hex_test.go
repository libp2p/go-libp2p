package libp2pwebrtc

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeInterspersedHex(t *testing.T) {
	b, err := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	require.NoError(t, err)
	require.Equal(t, "ba:78:16:bf:8f:01:cf:ea:41:41:40:de:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:ff:61:f2:00:15:ad", encodeInterspersedHex(b))
}

func BenchmarkEncodeInterspersedHex(b *testing.B) {
	data, err := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		encodeInterspersedHex(data)
	}
}

func TestDecodeInterpersedHexStringLowerCase(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("ba:78:16:bf:8f:01:cf:ea:41:41:40:de:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:ff:61:f2:00:15:ad")
	require.NoError(t, err)
	require.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", hex.EncodeToString(b))
}

func TestDecodeInterpersedHexStringMixedCase(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("Ba:78:16:BF:8F:01:cf:ea:41:41:40:De:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:FF:61:f2:00:15:ad")
	require.NoError(t, err)
	require.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", hex.EncodeToString(b))
}

func TestDecodeInterpersedHexStringOneByte(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("ba")
	require.NoError(t, err)
	require.Equal(t, "ba", hex.EncodeToString(b))
}

func TestDecodeInterpersedHexBytesLowerCase(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("ba:78:16:bf:8f:01:cf:ea:41:41:40:de:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:ff:61:f2:00:15:ad")
	require.NoError(t, err)
	require.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", hex.EncodeToString(b))
}

func BenchmarkDecode(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := decodeInterspersedHexFromASCIIString("ba:78:16:bf:8f:01:cf:ea:41:41:40:de:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:ff:61:f2:00:15:ad")
		require.NoError(b, err)
	}
}

func TestDecodeInterpersedHexBytesMixedCase(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("Ba:78:16:BF:8F:01:cf:ea:41:41:40:De:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:FF:61:f2:00:15:ad")
	require.NoError(t, err)
	require.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", hex.EncodeToString(b))
}

func TestDecodeInterpersedHexBytesOneByte(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("ba")
	require.NoError(t, err)
	require.Equal(t, "ba", hex.EncodeToString(b))
}

func TestEncodeInterperseHexNilSlice(t *testing.T) {
	require.Equal(t, "", encodeInterspersedHex(nil))
	require.Equal(t, "", encodeInterspersedHex([]byte{}))
}

func TestDecodeInterspersedHexEmpty(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("")
	require.NoError(t, err)
	require.Equal(t, []byte{}, b)
}

func TestDecodeInterpersedHexFromASCIIStringEmpty(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("")
	require.NoError(t, err)
	require.Equal(t, []byte{}, b)
}

func TestDecodeInterpersedHexInvalid(t *testing.T) {
	for _, v := range []string{"0", "0000", "000"} {
		_, err := decodeInterspersedHexFromASCIIString(v)
		require.Error(t, err)
	}
}

func TestDecodeInterpersedHexValid(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("00")
	require.NoError(t, err)
	require.Equal(t, []byte{0}, b)
}

func TestDecodeInterpersedHexFromASCIIStringInvalid(t *testing.T) {
	for _, v := range []string{"0", "0000", "000"} {
		_, err := decodeInterspersedHexFromASCIIString(v)
		require.Error(t, err)
	}
}

func TestDecodeInterpersedHexFromASCIIStringValid(t *testing.T) {
	b, err := decodeInterspersedHexFromASCIIString("00")
	require.NoError(t, err)
	require.Equal(t, []byte{0}, b)
}

func FuzzInterpersedHex(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		decoded, err := decodeInterspersedHexFromASCIIString(string(b))
		if err != nil {
			return
		}
		encoded := encodeInterspersedHex(decoded)
		require.Equal(t, strings.ToLower(string(b)), encoded)
	})
}

func FuzzInterspersedHexASCII(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) {
		decoded, err := decodeInterspersedHexFromASCIIString(s)
		if err != nil {
			return
		}
		encoded := encodeInterspersedHex(decoded)
		require.Equal(t, strings.ToLower(s), encoded)
	})
}
