module github.com/libp2p/go-libp2p/examples/pubsub/chat

go 1.16

require (
	github.com/gdamore/tcell/v2 v2.1.0
	github.com/libp2p/go-libp2p v0.14.1
	github.com/libp2p/go-libp2p-core v0.9.0
	github.com/libp2p/go-libp2p-pubsub v0.5.3
	github.com/rivo/tview v0.0.0-20210125085121-dbc1f32bb1d0
)

// Ensure that examples always use the go-libp2p version in the same git checkout.
replace github.com/libp2p/go-libp2p => ../../..
