# go-libp2p-pubsub chat example

This example project builds a chat room application using go-libp2p-pubsub. The app runs in the terminal,
and uses a text UI to show messages from other peers:

![An animation showing three terminal windows, each running the example application.](./chat-example.gif)

The goal of this example is to demonstrate the basic usage of the `PubSub` API, without getting into
the details of configuration.

## Running

Clone this repo, then `cd` into the `examples/pubsub/chat` directory:

```shell
git clone https://github.com/libp2p/go-libp2p
cd go-libp2p/examples/pubsub/chat
```

Now you can either run with `go run`, or build and run the binary:

```shell
go run .

# or, build and run separately
go build .
./chat
```

To set a nickname, use the `-nick` flag:

```shell
go run . -nick=zoidberg
```

You can join a specific chat room with the `-room` flag:

```shell
go run . -room=planet-express
```

It's usually more fun to chat with others, so open a new terminal and run the app again.
If you set a custom chat room name with the `-room` flag, make sure you use the same one
for both apps. Once the new instance starts, the two chat apps should discover each other 
automatically using mDNS, and typing a message into one app will send it to any others that are open.

To quit, hit `Ctrl-C`, or type `/quit` into the input field.

## Code Overview

In [`main.go`](./main.go), we create a new libp2p `Host` and then create a new `PubSub` service
using the GossipSub router:

```go
func main() {
	// (omitted) parse flags, etc...

	// create a new libp2p Host that listens on a random TCP port
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		panic(err)
	}

	// create a new PubSub service using the GossipSub router
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		panic(err)
	}

   // (omitted) setup mDNS discovery...
   
}
``` 

We configure the host to use local mDNS discovery, so that we can find other peers to chat with
on the local network. We also parse a few command line flags, so we can set a friendly nickname,
or choose a chat room by name.

Once we have a `Host` with an attached `PubSub` service, we join a `ChatRoom`:

```go
    // still in the main func
    cr, err := JoinChatRoom(ctx, ps, h.ID(), nick, room)
  	if err != nil {
  		panic(err)
  	}
```
 
`ChatRoom` is a custom struct defined in [`chatroom.go`](./chatroom.go):

```go
// ChatRoom represents a subscription to a single PubSub topic. Messages
// can be published to the topic with ChatRoom.Publish, and received
// messages are pushed to the Messages channel.
type ChatRoom struct {
	// Messages is a channel of messages received from other peers in the chat room
	Messages chan *ChatMessage

	ctx   context.Context
	ps    *pubsub.PubSub
	topic *pubsub.Topic
	sub   *pubsub.Subscription

	roomName string
	self     peer.ID
	nick     string
}
```

A `ChatRoom` subscribes to a PubSub `Topic`, and reads messages from the `Subscription`. We're sending our messages
wrapped inside of a `ChatMessage` struct:

```go
type ChatMessage struct {
	Message    string
	SenderID   string
	SenderNick string
}
```

This lets us attach friendly nicknames to the messages for display. A real app might want to make sure that
nicks are unique, but we just let anyone claim whatever nick they want and send it along with their messages.

The `ChatMessage`s are encoded to JSON and published to the PubSub topic, in the `Data` field of a `pubsub.Message`.
We could have used any encoding, as long as everyone in the topic agrees on the format, but JSON is simple and good 
enough for our purposes. 

To send messages, we have a `Publish` method, which wraps messages in `ChatMessage` structs, encodes them, and publishes 
to the `pubsub.Topic`:

```go
func (cr *ChatRoom) Publish(message string) error {
	m := ChatMessage{
		Message:    message,
		SenderID:   cr.self.String(),
		SenderNick: cr.nick,
	}
	msgBytes, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return cr.topic.Publish(cr.ctx, msgBytes)
}
```

In the background, the `ChatRoom` runs a `readLoop` goroutine, which reads messages from the `pubsub.Subscription`,
decodes the `ChatMessage` JSON, and sends the `ChatMessage`s on a channel:

```go
func (cr *ChatRoom) readLoop() {
	for {
		msg, err := cr.sub.Next(cr.ctx)
		if err != nil {
			close(cr.Messages)
			return
		}
		// only forward messages delivered by others
		if msg.ReceivedFrom == cr.self {
			continue
		}
		cm := new(ChatMessage)
		err = json.Unmarshal(msg.Data, cm)
		if err != nil {
			continue
		}
		// send valid messages onto the Messages channel
		cr.Messages <- cm
	}
}
```

There's also a `ListPeers` method, which just wraps the method of the same name in the `PubSub` service:

```go
func (cr *ChatRoom) ListPeers() []peer.ID {
	return cr.ps.ListPeers(topicName(cr.roomName))
}
```

That's pretty much it for the `ChatRoom`! 

Back in `main.go`, once we've created our `ChatRoom`, we pass it
to `NewChatUI`, which constructs a three panel text UI for entering and viewing chat messages, because UIs
are fun.

The `ChatUI` is defined in [`ui.go`](./ui.go), and the interesting bit is in the `handleEvents` event loop
method:

```go
func (ui *ChatUI) handleEvents() {
	peerRefreshTicker := time.NewTicker(time.Second)
	defer peerRefreshTicker.Stop()

	for {
		select {
		case input := <-ui.inputCh:
			// when the user types in a line, publish it to the chat room and print to the message window
			err := ui.cr.Publish(input)
			if err != nil {
				printErr("publish error: %s", err)
			}
			ui.displaySelfMessage(input)

		case m := <-ui.cr.Messages:
			// when we receive a message from the chat room, print it to the message window
			ui.displayChatMessage(m)

		case <-peerRefreshTicker.C:
			// refresh the list of peers in the chat room periodically
			ui.refreshPeers()

		case <-ui.cr.ctx.Done():
			return

		case <-ui.doneCh:
			return
		}
	}
}
```
