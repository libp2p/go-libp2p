package tcpreuse

import "testing"

func FuzzClash(f *testing.F) {
	// make untyped literals type correctly
	add := func(a, b, c byte) { f.Add(a, b, c) }

	// multistream-select
	add('\x13', '/', 'm')
	// http
	add('G', 'E', 'T')
	add('H', 'E', 'A')
	add('P', 'O', 'S')
	add('P', 'U', 'T')
	add('D', 'E', 'L')
	add('C', 'O', 'N')
	add('O', 'P', 'T')
	add('T', 'R', 'A')
	add('P', 'A', 'T')
	// tls
	add('\x16', '\x03', '\x01')
	add('\x16', '\x03', '\x02')
	add('\x16', '\x03', '\x03')
	add('\x16', '\x03', '\x04')

	f.Fuzz(func(t *testing.T, a, b, c byte) {
		s := Prefix{a, b, c}
		var total uint

		ms := IsMultistreamSelect(s)
		if ms {
			total++
		}

		http := IsHTTP(s)
		if http {
			total++
		}

		tls := IsTLS(s)
		if tls {
			total++
		}

		if total > 1 {
			t.Errorf("clash on: %q; ms: %v; http: %v; tls: %v", s, ms, http, tls)
		}
	})
}
