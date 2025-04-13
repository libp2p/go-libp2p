package libp2ptls

var extensionPrefix = []int{1, 3, 6, 1, 4, 1, 53594}

// getPrefixedExtensionID returns an Object Identifier
// that can be used in x509 Certificates.
func getPrefixedExtensionID(suffix []int) []int {
	return append(extensionPrefix, suffix...)
}
