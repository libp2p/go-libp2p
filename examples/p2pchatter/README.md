# P2P Chatter – A Simple Peer-to-Peer Chat in Go with libp2p

This project demonstrates a minimal peer-to-peer (P2P) chat application using [libp2p](https://github.com/libp2p/go-libp2p) in Go. Each node can connect to another using a multiaddress and chat directly—no central server required!

---

## 🚀 How to Run

### 1. **Start the First Node**

Open a terminal and run:

```sh
go run main.go
```

You’ll see output like:

```
Node started with Peer ID Qm...
Listening addressess: 
- /ip4/127.0.0.1/tcp/12345/p2p/Qm...
```

**Copy one of the printed addresses** (preferably the `/ip4/127.0.0.1/tcp/...` one).

---

### 2. **Start the Second Node**

Open another terminal and run:

```sh
go run main.go /ip4/127.0.0.1/tcp/12345/p2p/Qm...
```

Replace the address with the one you copied from the first node.

---

### 3. **Chat!**

Type messages in either terminal and see them appear in the other.  
You can start more nodes and connect them similarly.

---

## 📝 Code Walkthrough

### 1. **Host Creation**

```go
host, err := libp2p.New()
```
Creates a new libp2p node (host) with a unique Peer ID and listens on random TCP ports.

---

### 2. **Printing Addresses**

```go
for _, addr := range host.Addrs() {
    fmt.Printf("- %s/p2p/%s\n", addr, host.ID())
}
```
Prints all multiaddresses where this node can be reached.

---

### 3. **Stream Handler**

```go
host.SetStreamHandler(chatProtocol, handleStream)
```
Registers a handler for incoming chat streams using the `/p2pchat/1.0.0` protocol.

---

### 4. **Connecting to a Peer**

```go
if len(os.Args) > 1 {
    peerAddr, _ := ma.NewMultiaddr(os.Args[1])
    info, _ := peer.AddrInfoFromP2pAddr(peerAddr)
    host.Connect(ctx, *info)
    st, _ := host.NewStream(ctx, info.ID, chatProtocol)
    handleStream(st)
}
```
If a multiaddress is provided as a command-line argument, the node connects to that peer and opens a chat stream.

---

### 5. **Chat Logic**

#### **Receiving Messages**
```go
go func() {
    for {
        str, err := rw.ReadString('\n')
        if err != nil { return }
        if str != "" {
            fmt.Printf("\x1b[32m%s\x1b[0m", str)
        }
    }
}()
```
Reads incoming messages from the stream and prints them in green.

#### **Sending Messages**
```go
go func() {
    stdReader := bufio.NewReader(os.Stdin)
    for {
        sendData, _ := stdReader.ReadString('\n')
        rw.WriteString(sendData)
        rw.Flush()
    }
}()
```
Reads user input from the terminal and sends it to the connected peer.

---

## 🛠️ Requirements

- Go 1.18 or newer
- Internet access (for downloading dependencies)

---

## ⚡️ Notes

- All chat is in-memory and ephemeral—no message history or persistence.
- You can extend this to support more peers, message history, or a GUI!

---

Happy chatting! 🚀