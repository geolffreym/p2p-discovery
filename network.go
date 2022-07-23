package noise

import (
	"context"
	"log"
	"net"
	"time"
)

// Default protocol
const PROTOCOL = "tcp"

// futureDeadline calculate a new time for deadline since now.
func futureDeadLine(deadline time.Duration) time.Time {
	return time.Now().Add(deadline * time.Second)
}

type network struct {
	sentinel chan bool // Channel flag waiting for signal to close connection.
	router   *router   // Routing hash table eg. {Socket: Conn interface}.
	events   *events   // Pubsub notifications.
	config   Config    // Configuration settings
}

// newNetwork create a new node with default
func newNetwork(config Config) *network {
	return &network{
		make(chan bool),
		newRouter(),
		newEvents(),
		config,
	}
}

// Events proxy channels to subscriber.
// The listening routine should be stopped using context param.
func (n *network) Events(ctx context.Context) <-chan Message {
	ch := make(chan Message)
	go n.events.Subscriber().Listen(ctx, ch)
	return ch // read only channel <-chan
}

// SendMessage emit a new message to socket.
// If socket doesn't exists or peer is not connected return error.
// Calling SendMessage extends write deadline.
func (n *network) SendMessage(socket Socket, message []byte) (int, error) {
	peer := n.router.Query(socket)
	if peer == nil {
		return 0, ErrSendingMessage(socket)
	}

	bytes, err := peer.Write(message)
	// An idle timeout can be implemented by repeatedly extending
	// the deadline after successful Read or Write calls.
	// SetWriteDeadline sets the deadline for future Write calls
	// and any currently-blocked Write call.
	// Even if write times out, it may return n > 0, indicating that
	// some of the data was successfully written.
	idle := futureDeadLine(n.config.PeerDeadline())
	peer.SetWriteDeadline(idle)
	return bytes, err
}

// watch keep running waiting for incoming messages.
// After every new message the connection is verified, if local connection is closed or remote peer is disconnected the watch routine is stopped.
// Incoming message monitor is suggested to be processed in go routines.
func (n *network) watch(peer *Peer) {

KEEPALIVE:
	for {

		// Waiting for new incoming message
		buf, err := peer.Listen()
		// If connection is closed
		if n.Closed() {
			// stop routines watching for peers
			return
		}

		if err != nil {
			// net: don't return io.EOF from zero byte reads
			// Notify about the remote peer state
			n.events.PeerDisconnected([]byte(peer.Socket()))
			// Remove peer from router table
			n.router.Remove(peer)
			return
		}

		if buf == nil {
			// `buf` is nil if no more bytes received but peer is still connected
			// Keep alive always that zero bytes are not received
			break KEEPALIVE
		}

		// Emit new incoming message notification
		n.events.NewMessage(buf)

		// An idle timeout can be implemented by repeatedly extending
		// the deadline after successful Read or Write calls.
		// SetReadDeadline sets the deadline for future Read calls
		// and any currently-blocked Read call.
		idle := futureDeadLine(n.config.PeerDeadline())
		peer.SetReadDeadline(idle)

	}

}

// routing initialize route in routing table from connection interface.
// If TCP protocol is used connection is enforced to keep alive.
// It return new peer added to table.
func (n *network) routing(conn net.Conn) (*Peer, error) {

	// Assertion for tcp connection to keep alive
	connection, isTCP := conn.(*net.TCPConn)
	if isTCP {
		// If tcp enforce keep alive connection
		// SetKeepAlive sets whether the operating system should send keep-alive messages on the connection.
		connection.SetKeepAlive(true)
	}

	// Drop connections if max peers exceeded
	if n.router.Len() >= n.config.MaxPeersConnected() {
		log.Fatalf("max peers exceeded: MaxPeerConnected = %d", n.config.MaxPeersConnected())
		return nil, ErrExceededMaxPeers(n.config.MaxPeersConnected())
	}

	// Initial deadline for connection.
	// A deadline is an absolute time after which I/O operations
	// fail instead of blocking. The deadline applies to all future
	// and pending I/O, not just the immediately following call to
	// Read or Write. After a deadline has been exceeded, the
	// connection can be refreshed by setting a deadline in the future.
	// ref: https://pkg.go.dev/net#Conn
	idle := futureDeadLine(n.config.PeerDeadline())
	connection.SetDeadline(idle)
	// Routing connections
	remote := connection.RemoteAddr().String()
	// eg. 192.168.1.1:8080
	socket := Socket(remote)
	// We need to know how interact with peer based on socket and connection
	peer := newPeer(socket, connection)
	// Store new peer in router table
	n.router.Add(peer)
	return peer, nil
}

// Listen start listening on the given address and wait for new connection.
// Return error if error occurred while listening.
func (n *network) Listen(addr Socket) error {

	listener, err := net.Listen(PROTOCOL, addr)
	if err != nil {
		return err
	}

	// Dispatch event on start listening
	n.events.Listening([]byte(addr))
	//wait until sentinel channel is closed to close listener
	defer func() {
		err := listener.Close()
		if err != nil {
			log.Fatal(ErrClosingConnection(err).Error())
		}
	}()

	for {
		// Block/Hold while waiting for new incoming connection
		// Synchronized incoming connections
		conn, err := listener.Accept()
		// If connection is closed
		if n.Closed() {
			// Graceful stop listening
			return nil
		}

		if err != nil {
			log.Fatal(ErrBindingConnection(err).Error())
			return err
		}

		// Routing for accepted connection
		peer, err := n.routing(conn)
		if err != nil {
			conn.Close() // Drop connection
			continue
		}

		go n.watch(peer) // Wait for incoming messages
		// Dispatch event for new peer connected
		payload := []byte(peer.Socket())
		n.events.PeerConnected(payload)
	}

}

// Table return current routing table.
func (n *network) Table() Table {
	return n.router.Table()
}

// Closed check connection state.
// Return true for connection open else false.
func (n *network) Closed() bool {
	select {
	// select await for sentinel if not closed then default is returned.
	case <-n.sentinel:
		return true
	default:
		return false
	}
}

// Close all peers connections and stop listening
func (n *network) Close() {
	for _, peer := range n.router.Table() {
		go func(p *Peer) {
			if err := p.Close(); err != nil {
				log.Fatal(ErrClosingConnection(err).Error())
			}
		}(peer)
	}

	// Dispatch event on node get closed
	n.events.ClosedConnection()
	// If channel get closed then all routines waiting for connections
	// or waiting for incoming messages get closed too.
	close(n.sentinel)
}

// Dial attempt to connect to remote node and add connected peer to routing table.
// Return error if error occurred while dialing node.
func (n *network) Dial(addr Socket) error {
	conn, err := net.Dial(PROTOCOL, addr)
	if err != nil {
		return ErrDialingNode(err, addr)
	}

	// Routing for dialed connection
	peer, err := n.routing(conn)
	if err != nil {
		conn.Close() // Drop connection
		return ErrDialingNode(err, addr)
	}

	go n.watch(peer) // Wait for incoming messages
	// Dispatch event for new peer connected
	n.events.PeerConnected([]byte(peer.Socket()))
	return nil
}
