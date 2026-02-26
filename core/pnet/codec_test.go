package pnet

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func bufWithBase(base string, windows bool) *bytes.Buffer {
	b := &bytes.Buffer{}
	b.Write(pathPSKv1)
	if windows {
		b.WriteString("\r")
	}
	b.WriteString("\n")
	b.WriteString(base)
	if windows {
		b.WriteString("\r")
	}
	b.WriteString("\n")
	return b
}

func TestDecodeHex(t *testing.T) {
	testDecodeHex(t, true)
	testDecodeHex(t, false)
}

func TestDecodeBad(t *testing.T) {
	testDecodeBad(t, true)
	testDecodeBad(t, false)
}

func testDecodeBad(t *testing.T, windows bool) {
	b := bufWithBase("/verybadbase/", windows)
	b.WriteString("Have fun decoding that key")

	_, err := DecodeV1PSK(b)
	if err == nil {
		t.Fatal("expected 'unknown encoding' got nil")
	}
}

func testDecodeHex(t *testing.T, windows bool) {
	b := bufWithBase("/base16/", windows)
	for range 32 {
		b.WriteString("FF")
	}

	psk, err := DecodeV1PSK(b)
	if err != nil {
		t.Fatal(err)
	}

	for _, b := range psk {
		if b != 255 {
			t.Fatal("byte was wrong")
		}
	}
}

func TestDecodeB64(t *testing.T) {
	testDecodeB64(t, true)
	testDecodeB64(t, false)
}

func testDecodeB64(t *testing.T, windows bool) {
	b := bufWithBase("/base64/", windows)
	key := make([]byte, 32)
	for i := range 32 {
		key[i] = byte(i)
	}

	e := base64.NewEncoder(base64.StdEncoding, b)
	_, err := e.Write(key)
	if err != nil {
		t.Fatal(err)
	}
	err = e.Close()
	if err != nil {
		t.Fatal(err)
	}

	psk, err := DecodeV1PSK(b)
	if err != nil {
		t.Fatal(err)
	}

	for i, b := range psk {
		if b != psk[i] {
			t.Fatal("byte was wrong")
		}
	}

}

func TestDecodeBin(t *testing.T) {
	testDecodeBin(t, true)
	testDecodeBin(t, false)
}

func testDecodeBin(t *testing.T, windows bool) {
	b := bufWithBase("/bin/", windows)
	key := make([]byte, 32)
	for i := range 32 {
		key[i] = byte(i)
	}

	b.Write(key)

	psk, err := DecodeV1PSK(b)
	if err != nil {
		t.Fatal(err)
	}

	for i, b := range psk {
		if b != psk[i] {
			t.Fatal("byte was wrong")
		}
	}

}
