package client

import (
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/cryptowatch/proto/client"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

type State int

const (
	StateAny = -1

	StateDisconnected State = iota
	StateConnecting
	StateConnected

	StatesCnt
)

var (
	StateNames = make([]string, StatesCnt)
)

func init() {
	StateNames[StateDisconnected] = "disconnected"
	StateNames[StateConnecting] = "connecting"
	StateNames[StateConnected] = "connected"
}

// StreamParams contains params for opening a client stream connection
// (see StreamConn)
type StreamParams struct {
	Host          string
	NoTLS         bool
	Reconnect     bool
	Subscriptions []string
}

// StreamConn is a client stream connection; it's typically wrapped into more
// specific type of connection: e.g. MarketConn, which knows how to unmarshal
// the data being received.
type StreamConn struct {
	params         StreamParams
	wsConn         *websocket.Conn
	stateListeners map[State][]stateListener
	state          State
	onReadCB       onReadCallback

	mtx sync.Mutex
}

// stateListener wraps a state change callback and a flag of whether the
// callback is one-off (one-off listeners are only called once, on the next
// event)
type stateListener struct {
	cb  StateCallback
	opt StateListenerOpt
}

// NewStreamConn creates a new stream connection. Typically not used directly
// by the clients; see more concrete connection types, e.g. NewMarketConn.
//
// Note that clients should manually call Connect on a newly created
// connection; the rationale is that clients might register some state and/or
// message handlers before the connection, to avoid any possible races.
func NewStreamConn(params *StreamParams) (*StreamConn, error) {
	conn := &StreamConn{
		// Copy params defensively
		params: *params,

		state:          StateDisconnected,
		stateListeners: make(map[State][]stateListener),
	}

	// When connected, will send client identification message
	conn.AddStateListener(StateConnected, sendClientID)

	// Setup disconnection listener which will try to reconnect
	if params.Reconnect {
		conn.AddStateListener(StateDisconnected, reconnect)
	}

	return conn, nil
}

type StateCallback func(conn *StreamConn, oldState, state State)

// AddStateListener registers a new listener for the given state. Listener is
// registered with the default options (zero values of all fields in
// StateListenerOpt)
func (c *StreamConn) AddStateListener(state State, cb StateCallback) {
	c.AddStateListenerOpt(state, cb, StateListenerOpt{})
}

type StateListenerOpt struct {
	// If OneOff is true, the listener will only be called once; otherwise it'll
	// be called every time the requested state becomes active.
	OneOff bool
	// If CallImmediately is true, and the state being subscribed to is active
	// at the moment, the callback will be called immediately (with the "old"
	// state being equal to the new one)
	CallImmediately bool
}

// AddStateListenerOpt is like AddStateListener, but also takes additional
// options; see StateListenerOpt for details.
func (c *StreamConn) AddStateListenerOpt(state State, cb StateCallback, opt StateListenerOpt) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	// Determine whether the callback should be called right now
	callNow := opt.CallImmediately && c.state == state

	// Update stored listeners if needed
	if !opt.OneOff || !callNow {
		c.stateListeners[state] = append(c.stateListeners[state], stateListener{
			cb:  cb,
			opt: opt,
		})
	}

	if callNow {
		cb(c, state, state)
	}
}

// Connect tries to establish the websocket connection. Upon calling this
// function, the state will become StateConnecting, and before the function
// returns, the state will either be StateConnected in case of success,
// or StateDisconnected otherwise.
func (c *StreamConn) Connect() error {
	c.updateState(StateConnecting)

	var err error

	scheme := "wss"
	if c.params.NoTLS {
		scheme = "ws"
	}

	u := url.URL{Scheme: scheme, Host: c.params.Host, Path: ""}

	c.wsConn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		c.updateState(StateDisconnected)
		return errors.Annotatef(err, "connecting to stream server at %s", u.String())
	}

	c.updateState(StateConnected)

	go c.readLoop()

	return nil
}

// Subscribe subscribes to the given set of keys
func (c *StreamConn) Subscribe(keys []string) error {
	cm := &ProtobufClient.ClientMessage{
		Body: &ProtobufClient.ClientMessage_Subscribe{
			Subscribe: &ProtobufClient.ClientSubscribeMessage{
				SubscriptionKeys: keys,
			},
		},
	}

	if err := c.sendProto(cm); err != nil {
		return errors.Annotatef(err, "subscribe")
	}

	return nil
}

// Unsubscribe unsubscribes from the given set of keys
func (c *StreamConn) Unsubscribe(keys []string) error {
	cm := &ProtobufClient.ClientMessage{
		Body: &ProtobufClient.ClientMessage_Unsubscribe{
			Unsubscribe: &ProtobufClient.ClientUnsubscribeMessage{
				SubscriptionKeys: keys,
			},
		},
	}

	if err := c.sendProto(cm); err != nil {
		return errors.Annotatef(err, "unsubscribe")
	}

	return nil
}

type onReadCallback func(conn *StreamConn, data []byte)

func (c *StreamConn) onRead(cb onReadCallback) {
	c.onReadCB = cb
}

// send sends data to the websocket if it's connected, or queues the data
// otherwise.
func (c *StreamConn) send(data []byte) error {
	if c.state == StateConnected {
		// Socket is ready, send data now
		if err := c.wsConn.WriteMessage(websocket.TextMessage, data); err != nil {
			return errors.Annotatef(err, "sending msg")
		}
	} else {
		return errors.Errorf("not connected")
	}

	return nil
}

// sendProto marshals and sends a protobuf message to the websocket if it's
// connected, or queues the data otherwise.
func (c *StreamConn) sendProto(pb proto.Message) error {
	data, err := proto.Marshal(pb)
	if err != nil {
		return errors.Annotatef(err, "marshalling protobuf msg")
	}

	if err := c.send(data); err != nil {
		return errors.Trace(err)
	}

	return nil
}

func (c *StreamConn) callStateListeners(listeners []stateListener, oldState, state State) []stateListener {
	// Create a new slice of listeners and populate it with the permanent ones as
	// we go
	newListeners := []stateListener{}

	for _, sl := range listeners {
		sl.cb(c, oldState, state)
		if !sl.opt.OneOff {
			newListeners = append(newListeners, sl)
		}
	}

	return newListeners
}

func (c *StreamConn) updateState(state State) {
	if c.state == state {
		return
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	oldState := c.state
	c.state = state

	// Call listeners for that particular state and also for "any state".
	c.stateListeners[state] = c.callStateListeners(c.stateListeners[state], oldState, state)
	c.stateListeners[StateAny] = c.callStateListeners(c.stateListeners[StateAny], oldState, state)
}

func (c *StreamConn) readLoop() {
readloop:
	for {
		msgType, data, err := c.wsConn.ReadMessage()
		if err != nil {
			log.Println("read error:", err)
			break readloop
		}

		switch msgType {
		case websocket.TextMessage, websocket.BinaryMessage:
			if len(data) == 1 && data[0] == 0x01 {
				// Heartbeat, ignore
				continue
			}

			// Call on-read callback, if any
			if c.onReadCB != nil {
				c.onReadCB(c, data)
			}

		case websocket.CloseMessage:
			log.Println("got close message")
			break readloop
		}
	}

	c.updateState(StateDisconnected)
}

func reconnect(c *StreamConn, oldState, new State) {
	log.Println("Will reconnect...")
	// TODO: backoff
	time.AfterFunc(time.Second*3, func() {
		log.Println("Reconnecting...")
		c.Connect()
	})
}

func sendClientID(c *StreamConn, oldState, new State) {
	cm := &ProtobufClient.ClientIdentificationMessage{
		Useragent:     "Unknown user-agent",
		Revision:      "foo",
		Integration:   "bar",
		Locale:        "en_US", // TODO
		Subscriptions: c.params.Subscriptions,
	}

	if err := c.sendProto(cm); err != nil {
		// Not much we can do about the error here; and we shouldn't set state to
		// Disconnected because it should be already done by the reader goroutine
	}

	log.Println("sent client id")
}
