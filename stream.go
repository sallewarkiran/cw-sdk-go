package streamclient

import (
	"context"
	"fmt"
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
	StateDisconnected State = iota
	StateWaitBeforeReconnect
	StateConnecting
	StateConnected

	StatesCnt

	StateAny = -1
)

var (
	StateNames = make([]string, StatesCnt)

	ErrNotConnected = errors.New("not connected")
)

func init() {
	StateNames[StateDisconnected] = "disconnected"
	StateNames[StateWaitBeforeReconnect] = "wait_before_reconnect"
	StateNames[StateConnecting] = "connecting"
	StateNames[StateConnected] = "connected"
}

// StreamParams contains params for opening a client stream connection
// (see StreamConn)
type StreamParams struct {
	Host          string
	Path          string
	NoTLS         bool
	Reconnect     bool
	Subscriptions []string

	ReconnectTimeout time.Duration
	Backoff          bool
}

// StreamConn is a client stream connection; it's typically wrapped into more
// specific type of connection: e.g. MarketConn, which knows how to unmarshal
// the data being received.
type StreamConn struct {
	params StreamParams

	connTx        chan websocketTx
	callListeners chan callListenersReq

	stateListeners map[State][]stateListener

	// Current state
	state State
	// Error caused the current state; only relevant for StateDisconnected and
	// StateWaitBeforeReconnect, for other states it's always nil.
	stateCause error

	// onReadCB, if not nil, is called for each received websocket message.
	onReadCB onReadCallback

	// connCtx and connCtxCancel are context and its cancel func for the
	// currently running connLoop. If no connLoop is running at the moment, these
	// are nil.
	connCtx       context.Context
	connCtxCancel context.CancelFunc

	// wsConn is the currently active websocket connection, or nil if no
	// connection is established.
	wsConn *websocket.Conn

	mtx sync.Mutex
}

// stateListener wraps a state change callback and a flag of whether the
// callback is one-off (one-off listeners are only called once, on the next
// event)
type stateListener struct {
	cb  StateCallback
	opt StateListenerOpt
}

// websocketTx represents message to send to the websocket
type websocketTx struct {
	messageType int
	data        []byte
	res         chan error
}

type callListenersReq struct {
	listeners       []stateListener
	oldState, state State
	cause           error
}

// NewStreamConn creates a new stream connection. Typically not used directly
// by the clients; see more concrete connection types, e.g. NewMarketConn.
//
// Note that clients should manually call Connect on a newly created
// connection; the rationale is that clients might register some state and/or
// message handlers before the connection, to avoid any possible races.
func NewStreamConn(params *StreamParams) (*StreamConn, error) {
	c := &StreamConn{
		// Copy params defensively
		params: *params,

		state:          StateDisconnected,
		stateListeners: make(map[State][]stateListener),
		connTx:         make(chan websocketTx, 1),
		callListeners:  make(chan callListenersReq, 1),
	}

	// When connected, will send client identification message
	c.AddStateListener(StateConnected, sendClientID)

	// Start writeLoop right away, before even connecting, so that an attempt to
	// write something while not connected will result in a proper error.
	go c.writeLoop()

	// Start goroutine which will call state listeners. See comment for
	// notifyLoop for the rationale of this design.
	go c.notifyLoop()

	return c, nil
}

type StateCallback func(conn *StreamConn, oldState, state State, cause error)

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
	var callNow bool
	var stateCause error

	c.mtx.Lock()
	defer func() {
		c.mtx.Unlock()
		if callNow {
			cb(c, state, state, stateCause)
		}
	}()

	// Determine whether the callback should be called right now
	callNow = opt.CallImmediately && c.state == state
	stateCause = c.stateCause

	// Update stored listeners if needed
	if !opt.OneOff || !callNow {
		c.stateListeners[state] = append(c.stateListeners[state], stateListener{
			cb:  cb,
			opt: opt,
		})
	}
}

// Connect starts a connection goroutine, which establishes the connection,
// receives all messages, and reconnects if needed. The Connect function
// returns immediately, it doesn't wait for the connection to establish.
func (c *StreamConn) Connect() error {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	if c.state != StateDisconnected {
		// TODO: if state is StateWaitBeforeReconnect, connect immediately
		return errors.Errorf("connection is already active")
	}

	// NOTE that we need to enter the state StateConnecting here and not in
	// connLoop, in order to prevent the race which would result in multiple
	// running connLoops.
	c.connCtx, c.connCtxCancel = context.WithCancel(context.Background())
	c.updateState(StateConnecting, nil)

	go c.connLoop(c.connCtx, c.connCtxCancel)

	return nil
}

// Close stops reconnection loop (if reconnection was requested), and if
// websocket connection is active at the moment, closes it as well.
func (c *StreamConn) Close() error {
	if err := c.closeInternal(websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), true); err != nil {
		return errors.Trace(err)
	}

	return nil
}

func (c *StreamConn) closeInternal(data []byte, stopReconnecting bool) error {
	c.mtx.Lock()
	wsConn := c.wsConn

	if c.state == StateDisconnected {
		c.mtx.Unlock()
		return errors.Trace(ErrNotConnected)
	}

	// If asked to stop reconnection, cancel the conn context, which will
	// cause connLoop to quit once the current websocket connection (if any)
	// is closed
	if stopReconnecting {
		c.connCtxCancel()
	}
	c.mtx.Unlock()

	// If websocket connection is active, close it, which will cause connLoop
	// break out of readLoop (and then either reconnect or quit, depending on the
	// stopReconnecting arg)
	if wsConn != nil {
		if err := wsConn.WriteControl(websocket.CloseMessage, data, time.Time{}); err != nil {
			// Graceful close failed, try to close forcefully
			return errors.Trace(wsConn.Close())
		}
	}

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

// onRead sets on-read callback; it should be called once right after creation
// of the StreamConn by a wrapper (like MarketConn), before the connection is
// established.
func (c *StreamConn) onRead(cb onReadCallback) {
	c.onReadCB = cb
}

// send sends data to the websocket if it's connected
func (c *StreamConn) send(data []byte) error {
	// Note that we don't check here whether the socket is connected,
	// as it's checked by the writeLoop() which will receive our message
	// from c.connTx.

	res := make(chan error)

	// Request the websocket write
	c.connTx <- websocketTx{
		messageType: websocket.TextMessage,
		data:        data,
		res:         res,
	}

	// Wait for the resulting error
	if err := <-res; err != nil {
		return errors.Annotatef(err, "sending msg")
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

// removeOneOff takes a slice of listeners and returns a new one, with one-off
// listeners removed.
func (c *StreamConn) removeOneOff(listeners []stateListener) []stateListener {
	// NOTE: c.mtx should be locked when removeOneOff is called

	newListeners := []stateListener{}

	for _, sl := range listeners {
		if !sl.opt.OneOff {
			newListeners = append(newListeners, sl)
		}
	}

	return newListeners
}

func (c *StreamConn) updateState(state State, cause error) {
	// NOTE: c.mtx should be locked when removeOneOff is called

	if c.state == state {
		// No need to call any listeners
		return
	}

	oldState := c.state
	c.state = state
	c.stateCause = cause

	// Collect all listeners to call now
	// We'll call them a bit later, when the mutex is not locked
	listeners := append(c.stateListeners[state], c.stateListeners[StateAny]...)

	// Remove one-off listeners
	c.stateListeners[state] = c.removeOneOff(c.stateListeners[state])
	c.stateListeners[StateAny] = c.removeOneOff(c.stateListeners[StateAny])

	c.callListeners <- callListenersReq{
		listeners: listeners,
		oldState:  oldState,
		state:     state,
		cause:     cause,
	}
}

// connLoop establishes a connection, then keeps receiving all websocket
// messages (and calls onReadCB for each of them) until the connection is
// closed, then either waits for a timeout and connects again, or just quits.
func (c *StreamConn) connLoop(connCtx context.Context, connCtxCancel context.CancelFunc) {
	var connErr error

	defer func() {
		c.mtx.Lock()
		defer c.mtx.Unlock()
		c.updateState(StateDisconnected, connErr)
		c.connCtx = nil
		c.connCtxCancel = nil
	}()

cloop:
	for {
		scheme := "wss"
		if c.params.NoTLS {
			scheme = "ws"
		}

		u := url.URL{Scheme: scheme, Host: c.params.Host, Path: c.params.Path}

		var wsConn *websocket.Conn
		wsConn, _, connErr = websocket.DefaultDialer.Dial(u.String(), nil)
		if connErr == nil {
			// Connected successfully

			c.mtx.Lock()
			c.wsConn = wsConn
			c.updateState(StateConnected, nil)
			c.mtx.Unlock()

			// Will loop here until the websocket connection is closed
		recvLoop:
			for {
				msgType, data, err := wsConn.ReadMessage()
				if err != nil {
					connErr = err
					break recvLoop
				}

				switch msgType {
				case websocket.TextMessage, websocket.BinaryMessage:
					if len(data) == 1 && data[0] == 0x01 {
						// Heartbeat, ignore
						continue recvLoop
					}

					// Call on-read callback, if any
					if c.onReadCB != nil {
						c.onReadCB(c, data)
					}

				case websocket.CloseMessage:
					break recvLoop
				}
			}
		}

		c.mtx.Lock()
		c.wsConn = nil
		c.mtx.Unlock()

		// If shouldn't reconnect, we're done
		if !c.params.Reconnect {
			connCtxCancel()
		}

		// Check if we need to enter state StateWaitBeforeReconnect
		select {
		case <-connCtx.Done():
		default:
			// Looks like we should reconnect (after a timeout), so set the
			// appropriate state
			c.mtx.Lock()
			c.updateState(StateWaitBeforeReconnect, connErr)
			c.mtx.Unlock()
		}

		// Either wait for the timeout before reconnection, or quit.
		select {
		case <-connCtx.Done():
			// Enough reconnections, quit now.
			break cloop

			// TODO: backoff
		case <-time.After(c.params.ReconnectTimeout):
			// Will try to reconnect one more time
			c.mtx.Lock()
			c.updateState(StateConnecting, nil)
			c.mtx.Unlock()

			continue cloop
		}
	}
}

// writeLoop receives messages from c.connTx, and tries to send them
// to the active websocket connection, if any.
func (c *StreamConn) writeLoop() {
cloop:
	for {
		msg := <-c.connTx

		// Get currently active websocket connection
		c.mtx.Lock()
		wsConn := c.wsConn
		c.mtx.Unlock()

		if wsConn == nil {
			msg.res <- errors.Trace(ErrNotConnected)
			continue cloop
		}

		// Try to write the message
		err := errors.Trace(wsConn.WriteMessage(msg.messageType, msg.data))

		// Send resulting error to the requester
		msg.res <- err
	}
}

// notifyLoop calls state listeners. The rationale to have a separate goroutine
// for that is that the listeners should be called with c.mtx unlocked, and we
// don't want to unlock it in the middle of c.updateState()
func (c *StreamConn) notifyLoop() {
	for {
		req := <-c.callListeners

		for _, sl := range req.listeners {
			sl.cb(c, req.oldState, req.state, req.cause)
		}
	}
}

// sendClientID sends ClientIdentificationMessage, it's called on
// entering StateConnected state.
func sendClientID(c *StreamConn, oldState, new State, cause error) {
	cm := &ProtobufClient.ClientMessage{
		Body: &ProtobufClient.ClientMessage_Identification{
			Identification: &ProtobufClient.ClientIdentificationMessage{
				Useragent: fmt.Sprintf("Cryptowatch Stream Client Golang/%s", version),

				// TODO: probably pass revision via params so that it depends on a
				// client application rather than a library, or just leave it always
				// empty as it is now.
				//
				// It's not trivial to have a revision of the library repo, because it
				// should be go-gettable and importable by other code, and for that,
				// we'd need to include the revision constant into the repo. Obviously,
				// even if we do autogenerate some file with the revision on every
				// commit, it would actually represent previous commit, not the current
				// one, and this would be just weird.
				Revision:      "",
				Integration:   "",      // Irrelevant
				Locale:        "en_US", // TODO: get locale from the OS
				Subscriptions: c.params.Subscriptions,
			},
		},
	}

	if err := c.sendProto(cm); err != nil {
		// Not much we can do about the error here; and we shouldn't set state to
		// Disconnected because it should be already done by the reader goroutine
	}
}
