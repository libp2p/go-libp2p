package protocol

// ID is an identifier used to write protocol headers in streams.
type ID string

// These are reserved protocol.IDs.
const (
	TestingID ID = "/p2p/_testing"
)

// ConvertFromStrings is a convenience function that takes a slice of strings and
// converts it to a slice of protocol.ID.
func ConvertFromStrings(ids []string) (res []ID) {
	res = make([]ID, len(ids))
	for i, id := range ids {
		res[i] = ID(id)
	}
	return res
}

// ConvertToStrings is a convenience function that takes a slice of protocol.ID and
// converts it to a slice of strings.
func ConvertToStrings(ids []ID) (res []string) {
	res = make([]string, len(ids))
	for i, id := range ids {
		res[i] = string(id)
	}
	return res
}
