package wsclient // import "code.cryptowat.ch/ws-client-go"

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	pbc "code.cryptowat.ch/ws-client-go/proto/client"
	pbs "code.cryptowat.ch/ws-client-go/proto/stream"
	"code.cryptowat.ch/ws-client-go/internal"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

const (
	defaultURL = "wss://stream.cryptowat.ch"
)

// The following errors are returned from wsConn, which applies to both
// StreamClient and TradeClient.
var (
	// ErrNotConnected means the connection is not established when the client
	// tried to e.g. send a message, or close the connection.
	ErrNotConnected = errors.New("not connected")

	// ErrConnLoopActive means the client tried to connect when the client is
	// already connecting.
	ErrConnLoopActive = errors.New("connection loop is already active")

	// ErrBadCredentials means the provided APIKey and/or SecretKey were invalid.
	ErrBadCredentials = errors.New("bad credentials")

	// ErrTokenExpired means the authentication procedure took too long and the
	// token expired. The client retries authentication 3 times before actually
	// returning this error.
	ErrTokenExpired = errors.New("token is expired")

	// ErrBadNonce means the nonce used in the authentication request is not
	// larger than the last used nonce.
	ErrBadNonce = errors.New("bad nonce")

	// ErrUnknownAuthnError means some unexpected authentication problem;
	// possibly caused by an internal error on stream server.
	ErrUnknownAuthnError = errors.New("unknown authentication error")
)

// WSParams contains options for opening a websocket connection.
type WSParams struct {
	// APIKey and SecretKey are needed to authenticate with Cryptowatch back end.
	// Inquire about getting access here: https://docs.google.com/forms/d/e/1FAIpQLSdhv_ceVtKA0qQcW6zQzBniRBaZ_cC4al31lDCeZirntkmWQw/viewform?c=0&w=1
	APIKey    string
	SecretKey string

	// Subscriptions is a list of subscription keys for the client to subscribe to. The
	// client will subscribe when it is initially authenticated, or when it reconnects
	// after being disconnected.
	Subscriptions []string

	// URL is the URL to connect to over websockets. You will not have to set this
	// unless testing against a non-production environment since a default is
	// always used.
	URL string

	// ReconnectOpts contains settings for how to reconnect if the client becomes disconnected.
	// Sensible defaults are used.
	ReconnectOpts *ReconnectOpts
}

type wsConnParamsInternal struct {
	unmarshalAuthnResult func(data []byte) (*pbs.AuthenticationResult, error)
}

// ReconnectOpts are settings used to reconnect after being disconnected. By default, the client will reconnect
// with backoff starting with 0 seconds and increasing linearly up to 30 seconds. These are the most "aggressive"
// reconnect options permitted in the client. For example, you cannot set ReconnectTimeout to 0 and Backoff to false.
type ReconnectOpts struct {
	// Reconnect switch: if true, the client will attempt to reconnect to the websocket back
	// end if it is disconnected. If false, the client will stay disconnected.
	Reconnect bool

	// Reconnection backoff: if true, then the reconnection time will be
	// initially ReconnectTimeout, then will grow by 500ms on each unsuccessful
	// connection attempt; but it won't be longer than MaxReconnectTimeout.
	Backoff bool

	// Initial reconnection timeout: defaults to 0 seconds. If backoff=false,
	// a minimum reconnectTimeout of 1 second will be used.
	ReconnectTimeout time.Duration

	// Max reconnect timeout. If zero, then 30 seconds will be used.
	MaxReconnectTimeout time.Duration
}

var defaultReconnectOpts = &ReconnectOpts{
	Reconnect:           true,
	Backoff:             true,
	ReconnectTimeout:    0,
	MaxReconnectTimeout: 30 * time.Second,
}

// ConnState represents the websocket connection state
type ConnState int

// The following constants represent every possible ConnState.
const (
	// ConnStateDisconnected means we're disconnected and not trying to connect.
	// connLoop is not running.
	ConnStateDisconnected ConnState = iota

	// ConnStateWaitBeforeReconnect means we already tried to connect, but then
	// either the connection failed, or succeeded but later disconnected for some
	// reason (see stateCause), and now we're waiting for a timeout before
	// connecting again. wsConn is nil, but connCtx and connCtxCancel are not,
	// and connLoop is running.
	ConnStateWaitBeforeReconnect

	// ConnStateConnecting means we're calling websocket.DefaultDialer.Dial() right
	// now.
	ConnStateConnecting

	// ConnStateAuthenticating means the transport (websocket) connection is
	// established, and right now we're exchanging the authentication and
	// identification messages
	ConnStateAuthenticating

	// ConnStateEstablished means the connection is ready
	ConnStateEstablished

	// ConnStateAny can be used with AddStateListener() and AddStateListenerOpt()
	// in order to listen for all states.
	ConnStateAny = -1
)

// ConnStateNames contains human-readable names for connection states.
var ConnStateNames = map[ConnState]string{
	ConnStateDisconnected:        "disconnected",
	ConnStateWaitBeforeReconnect: "wait-before-reconnect",
	ConnStateConnecting:          "connecting",
	ConnStateAuthenticating:      "authenticating",
	ConnStateEstablished:         "established",
}

// wsConn represents a stream connection, it contains unexported fields
type wsConn struct {
	params         WSParams
	paramsInternal wsConnParamsInternal

	subscriptions map[string]struct{}

	transport *internal.StreamTransportConn

	authnCtx       context.Context
	authnCtxCancel context.CancelFunc

	// Current state
	state ConnState

	// Error caused the current state; only relevant for
	// TransportStateDisconnected and TransportStateWaitBeforeReconnect, for
	// other states it's always nil.
	stateCause error

	// actualCauseOfDisconnection is set whenever the client initiates the
	// disconnection; it's set to the specific error causing the disconnection.
	//
	// When the disconnection happens, and actualCauseOfDisconnection is not nil,
	// then this error is passed to the clients instead of the generic websocket
	// disconnection error.
	actualCauseOfDisconnection error

	stateListeners map[ConnState][]stateListener

	// onReadCB, if not nil, is called for each received websocket message.
	onReadCB onReadCallback

	// internalEvents is a channel of events handled by eventLoop. See
	// internalEvent struct.
	internalEvents chan internalEvent
}

// internalEvent represents an event handled in eventLoop. Each field
// represents one kind of the event, and only a single field should be non-nil.
type internalEvent struct {
	// rxData contains data received from the server via websocket.
	rxData []byte
	// transportStateUpdate represents an update of transport layer state.
	transportStateUpdate *transportStateUpdate

	// reqAddStateListener is the result of the clients call to AddStateListener
	// or AddStateListenerOpt.
	reqAddStateListener *reqAddStateListener
	reqConnState        *reqConnState
	reqSubscribe        *reqSubscribe
	reqUnsubscribe      *reqUnsubscribe
}

// reqAddStateListener is a request to add state listener
type reqAddStateListener struct {
	state ConnState
	cb    StateCallback
	opt   StateListenerOpt

	result chan<- struct{}
}

// reqConnState is a client request of conn state via ConnState().
type reqConnState struct {
	result chan<- ConnState
}

// reqSubscribe is a client request of conn state via Subscribe().
type reqSubscribe struct {
	keys   []string
	result chan<- error
}

// reqUnsubscribe is a client request of conn state via Unsubscribe().
type reqUnsubscribe struct {
	keys   []string
	result chan<- error
}

// transportStateUpdate is an update of transport layer state.
type transportStateUpdate struct {
	oldState internal.TransportState
	state    internal.TransportState

	cause error
}

// newWsConn creates a new websocket connection with the given params.
//
// Note that clients should manually call Connect on a newly created
// connection; the rationale is that clients might register some state and/or
// message handlers before the connection, to avoid any possible races.
func newWsConn(params *WSParams, paramsInternal *wsConnParamsInternal) (*wsConn, error) {
	p := *params

	if p.URL == "" {
		p.URL = defaultURL
	}

	if p.ReconnectOpts == nil {
		p.ReconnectOpts = defaultReconnectOpts
	}

	transport, err := internal.NewStreamTransportConn(&internal.StreamTransportParams{
		URL: p.URL,

		Reconnect:           p.ReconnectOpts.Reconnect,
		Backoff:             p.ReconnectOpts.Backoff,
		ReconnectTimeout:    p.ReconnectOpts.ReconnectTimeout,
		MaxReconnectTimeout: p.ReconnectOpts.MaxReconnectTimeout,
	})
	if err != nil {
		return nil, errors.Trace(err)
	}

	c := &wsConn{
		params:         p,
		paramsInternal: *paramsInternal,
		subscriptions:  make(map[string]struct{}),
		transport:      transport,
		stateListeners: make(map[ConnState][]stateListener),
		internalEvents: make(chan internalEvent, 8),
	}

	for _, sub := range params.Subscriptions {
		c.subscriptions[sub] = struct{}{}
	}

	transport.OnStateChange(
		func(_ *internal.StreamTransportConn, oldTransportState, transportState internal.TransportState, cause error) {
			c.internalEvents <- internalEvent{
				transportStateUpdate: &transportStateUpdate{
					oldState: oldTransportState,
					state:    transportState,
					cause:    cause,
				},
			}
		},
	)

	transport.OnRead(
		func(tc *internal.StreamTransportConn, data []byte) {
			c.internalEvents <- internalEvent{
				rxData: data,
			}
		},
	)

	// Start goroutine which will call state listeners
	go c.eventLoop()

	return c, nil
}

// onRead sets on-read callback; it should be called once right after creation
// of the wsConn by a wrapper (like StreamClient), before the connection is
// established.
func (c *wsConn) onRead(cb onReadCallback) {
	c.onReadCB = cb
}

// Connect either starts a connection goroutine (if state is
// ConnStateDisconnected), or makes it connect immediately, ignoring timeout
// (if the state is ConnStateWaitBeforeReconnect). For other states, this returns an
// error.
//
// Connect doesn't wait for the connection to establish; it returns immediately.
func (c *wsConn) Connect() (err error) {
	defer func() {
		// Translate internal transport errors to public ones
		if errors.Cause(err) == internal.ErrConnLoopActive {
			err = errors.Trace(ErrConnLoopActive)
		}
	}()

	if err := c.transport.Connect(); err != nil {
		return errors.Trace(err)
	}

	return nil
}

// disconnect sends a "normal closure" websocket message to the server,
// causing it to disconnect, and when we receive the actual disconnection soon,
// the cause error given to the clients will be the cause given to disconnect.
//
// If cause is nil, then the upcoming disconnection error will be passed to
// clients as is.
//
// NOTE: disconnect should only be called from the eventLoop.
func (c *wsConn) disconnect(cause error) {
	c.disconnectOpt(cause, websocket.CloseNormalClosure, "")
}

// disconnectOpt sends a websocket closure message (with given closeCode and
// text) to the server, causing it to disconnect, and when we receive the
// actual disconnection soon, the cause error given to the clients will be the
// cause given to disconnectOpt.
//
// If passed cause is nil, then the upcoming disconnection error will be passed
// to clients as is.
//
// NOTE: disconnectOpt should only be called from the eventLoop.
func (c *wsConn) disconnectOpt(cause error, closeCode int, text string) {
	closeErr := c.transport.CloseOpt(
		websocket.FormatCloseMessage(closeCode, text),
		false,
	)
	if closeErr != nil {
		return
	}

	c.actualCauseOfDisconnection = cause
}

// Close stops reconnection loop (if reconnection was requested), and if
// websocket connection is active at the moment, closes it as well
func (c *wsConn) Close() (err error) {
	defer func() {
		// Translate internal transport errors to public ones
		if errors.Cause(err) == internal.ErrNotConnected {
			err = errors.Trace(ErrNotConnected)
		}
	}()

	if err = c.transport.Close(); err != nil {
		return errors.Trace(err)
	}

	return nil
}

func (c *wsConn) handleAuthnResult(result *pbs.AuthenticationResult) error {
	// Got authentication result message
	switch result.Status {
	case pbs.AuthenticationResult_AUTHENTICATED:
		return nil

	case pbs.AuthenticationResult_TOKEN_EXPIRED:
		// The token is expired
		return ErrTokenExpired

	case pbs.AuthenticationResult_BAD_TOKEN:
		// User provided bad creds
		return ErrBadCredentials

	case pbs.AuthenticationResult_BAD_NONCE:
		return ErrBadNonce

	case pbs.AuthenticationResult_UNKNOWN:
		// Shouldn't happen normally; it can happen e.g. if stream has failed
		// to connect to biller, or failed to call biller's methods.
		return ErrUnknownAuthnError

	default:
		// Client problem
		return ErrUnknownAuthnError
	}
}

// StateCallback is a signature of a state listener. Arguments conn, oldState
// and state are self-descriptive; cause is the error which caused the current
// state. Cause is relevant only for ConnStateDisconnected and
// ConnStateWaitBeforeReconnect (in which case it's either the reason of failure to
// connect, or reason of disconnection), for other states it's always nil.
//
// See AddStateListener.
type StateCallback func(prevState, curState ConnState, cause error)

// onReadCallback is a signature of (internal) incoming messages listener.
type onReadCallback func(data []byte)

type StateListenerOpt struct {
	// If OneOff is true, the listener will only be called once; otherwise it'll
	// be called every time the requested state becomes active.
	OneOff bool

	// If CallImmediately is true, and the state being subscribed to is active
	// at the moment, the callback will be called immediately (with the "old"
	// state being equal to the new one)
	CallImmediately bool
}

// AddStateListener registers a new listener for the given state. Listener is
// registered with the default options (zero values of all fields in
// StateListenerOpt). All registered callbacks for all states (and all
// messages, see AddMarketListener) will be called by the same internal
// goroutine, i.e. they are never called concurrently with each other.
//
// The order of listeners invocation for the same state is unspecified, and
// clients shouldn't rely on it.
//
// The listeners shouldn't block; a blocked listener will cause the whole
// stream connection to stuck. If you need to block there, consider spawning a
// goroutine for that.
//
// To subscribe to all state changes, use ConnStateAny as a state.
func (c *wsConn) AddStateListener(state ConnState, cb StateCallback) {
	c.AddStateListenerOpt(state, cb, StateListenerOpt{})
}

// AddStateListenerOpt is like AddStateListener, but also takes additional
// options; see StateListenerOpt for details.
func (c *wsConn) AddStateListenerOpt(state ConnState, cb StateCallback, opt StateListenerOpt) {
	result := make(chan struct{})

	c.internalEvents <- internalEvent{
		reqAddStateListener: &reqAddStateListener{
			state: state,
			cb:    cb,
			opt:   opt,

			result: result,
		},
	}

	<-result
}

// ConnClosedCallback defines the callback function for OnConnClosed.
type ConnClosedCallback func(state ConnState, cause error)

// OnConnClosed allows the client to set a callback for when the connection is lost.
// The new state of the client could be ConnStateDisconnected or ConnStateWaitBeforeReconnect.
func (c *wsConn) OnConnClosed(cb ConnClosedCallback) {
	c.AddStateListener(ConnStateDisconnected, func(_, curState ConnState, cause error) {
		cb(curState, cause)
	})
	c.AddStateListener(ConnStateWaitBeforeReconnect, func(_, curState ConnState, cause error) {
		cb(curState, cause)
	})
}

// GetSubscriptions returns a slice of the current subscriptions.
func (c *wsConn) GetSubscriptions() []string {
	subs := []string{}
	for sub, _ := range c.subscriptions {
		subs = append(subs, sub)
	}
	return subs
}

// Subscribe subscribes to the given set of keys. Example:
//
//   conn.Subscribe([]string{
//           "market:1:book:deltas",
//           "market:1:book:spread",
//   })
//
// For that, the client should already be connected and authenticated.
// Note that the initial set of subscription keys can be given as params to
// newWsConn.
func (c *wsConn) Subscribe(keys []string) error {
	result := make(chan error, 1)

	c.internalEvents <- internalEvent{
		reqSubscribe: &reqSubscribe{
			keys:   keys,
			result: result,
		},
	}

	return <-result
}

// Unsubscribe unsubscribes from the given set of keys. Also see notes for
// Subscribe.
func (c *wsConn) Unsubscribe(keys []string) error {
	result := make(chan error, 1)

	c.internalEvents <- internalEvent{
		reqUnsubscribe: &reqUnsubscribe{
			keys:   keys,
			result: result,
		},
	}

	return <-result
}

// ConnState returns current client connection state.
func (c *wsConn) ConnState() ConnState {
	result := make(chan ConnState, 1)

	c.internalEvents <- internalEvent{
		reqConnState: &reqConnState{
			result: result,
		},
	}

	return <-result
}

// URL returns the url the client is connected to, e.g. wss://stream.cryptowat.ch.
func (c *wsConn) URL() string {
	return c.params.URL
}

// stateListener wraps a state change callback and a flag of whether the
// callback is one-off (one-off listeners are only called once, on the next
// event)
type stateListener struct {
	cb  StateCallback
	opt StateListenerOpt
}

type callStateListenersReq struct {
	listeners       []stateListener
	oldState, state ConnState
	cause           error
}

// NOTE: updateState should only be called from the eventLoop.
func (c *wsConn) updateState(state ConnState, cause error) {
	if c.state == state {
		// No need to do anything
		return
	}

	// Properly leave the current state
	c.enterLeaveState(c.state, false)

	oldState := c.state
	c.state = state
	c.stateCause = cause

	// Properly enter the new state
	c.enterLeaveState(c.state, true)

	// Collect all listeners to call now
	// We'll call them a bit later, when the mutex is not locked
	listeners := append(c.stateListeners[state], c.stateListeners[ConnStateAny]...)

	// Remove one-off listeners
	c.stateListeners[state] = removeOneOff(c.stateListeners[state])
	c.stateListeners[ConnStateAny] = removeOneOff(c.stateListeners[ConnStateAny])

	// Request listeners invocation (will be invoked by a dedicated goroutine)
	c.callStateListeners(&callStateListenersReq{
		listeners: listeners,
		oldState:  oldState,
		state:     state,
		cause:     cause,
	})
}

// enterLeaveState should be called on leaving and entering each state. So,
// when changing state from A to B, it's called twice, like this:
//
//      enterLeaveState(A, false)
//      enterLeaveState(B, true)
//
// NOTE: enterLeaveState should only be called from eventLoop.
func (c *wsConn) enterLeaveState(state ConnState, enter bool) {
	switch state {

	case ConnStateAuthenticating:
		// authnCtx and its cancel func should only be present in the
		// ConnStateAuthenticating state.
		if enter {
			c.authnCtx, c.authnCtxCancel = context.WithCancel(context.Background())
		} else {
			c.authnCtxCancel()

			c.authnCtx = nil
			c.authnCtxCancel = nil
		}
	}
}

// removeOneOff takes a slice of listeners and returns a new one, with one-off
// listeners removed.
func removeOneOff(listeners []stateListener) []stateListener {

	newListeners := []stateListener{}

	for _, sl := range listeners {
		if !sl.opt.OneOff {
			newListeners = append(newListeners, sl)
		}
	}

	return newListeners
}

// NOTE: subscribeInternal should only be called from eventLoop.
func (c *wsConn) subscribeInternal(keys []string) error {
	cm := &pbc.ClientMessage{
		Body: &pbc.ClientMessage_Subscribe{
			Subscribe: &pbc.ClientSubscribeMessage{
				SubscriptionKeys: keys,
			},
		},
	}

	if err := c.sendProto(context.Background(), cm); err != nil {
		return errors.Annotatef(err, "subscribe")
	}

	for _, sub := range keys {
		c.subscriptions[sub] = struct{}{}
	}

	return nil
}

// NOTE: unsubscribeInternal should only be called from eventLoop.
func (c *wsConn) unsubscribeInternal(keys []string) error {
	cm := &pbc.ClientMessage{
		Body: &pbc.ClientMessage_Unsubscribe{
			Unsubscribe: &pbc.ClientUnsubscribeMessage{
				SubscriptionKeys: keys,
			},
		},
	}

	if err := c.sendProto(context.Background(), cm); err != nil {
		return errors.Annotatef(err, "unsubscribe")
	}

	for _, sub := range keys {
		delete(c.subscriptions, sub)
	}

	return nil
}

// sendProto marshals and sends a protobuf message to the websocket if it's
// connected, or queues the data otherwise.
func (c *wsConn) sendProto(ctx context.Context, pb proto.Message) (err error) {
	defer func() {
		if errors.Cause(err) == internal.ErrNotConnected {
			err = errors.Trace(ErrNotConnected)
		}
	}()

	data, err := proto.Marshal(pb)
	if err != nil {
		return errors.Annotatef(err, "marshalling protobuf msg")
	}

	if err := c.transport.Send(ctx, data); err != nil {
		return errors.Trace(err)
	}

	return nil
}

// eventLoop handles all internal events like transport state change, received
// data, or client calls to add state listener. See internalEvent struct.
func (c *wsConn) eventLoop() {
loop:
	for {
		event := <-c.internalEvents

		if tsu := event.transportStateUpdate; tsu != nil {
			// Transport layer state changed.

			switch tsu.state {
			case
				internal.TransportStateDisconnected,
				internal.TransportStateWaitBeforeReconnect,
				internal.TransportStateConnecting:

				// Disconnected, WaitBeforeReconnect and Connecting are easy: they
				// translate 1-to-1 to the wsClient-level state.

				var state ConnState
				switch tsu.state {
				case internal.TransportStateDisconnected:
					state = ConnStateDisconnected
				case internal.TransportStateWaitBeforeReconnect:
					state = ConnStateWaitBeforeReconnect
				case internal.TransportStateConnecting:
					state = ConnStateConnecting
				default:
					// Should never be here
					panic(fmt.Sprintf("unexpected transport state: %d", tsu.state))
				}

				cause := tsu.cause
				if c.actualCauseOfDisconnection != nil {
					cause = c.actualCauseOfDisconnection
					c.actualCauseOfDisconnection = nil
				}

				c.updateState(state, errors.Trace(cause))

			case internal.TransportStateConnected:
				// When transport layer is Connected, we need to set wsClient state to
				// Authenticating, and send authentication data to the server.

				c.updateState(ConnStateAuthenticating, errors.Trace(tsu.cause))
				ctx := c.authnCtx

				nonce := getNonce()
				token, err := c.generateToken(nonce)
				if err != nil {
					c.disconnect(errors.Trace(err))
					continue loop
				}

				authMsg := &pbc.ClientMessage{
					Body: &pbc.ClientMessage_ApiAuthentication{
						ApiAuthentication: &pbc.APIAuthenticationMessage{
							Token:         token,
							Nonce:         nonce,
							ApiKey:        c.params.APIKey,
							Source:        pbc.APIAuthenticationMessage_GOLANG_SDK,
							Version:       version,
							Subscriptions: c.GetSubscriptions(),
						},
					},
				}

				if err := c.sendProto(ctx, authMsg); err != nil {
					c.disconnect(errors.Trace(err))
					continue loop
				}

			default:
				panic(fmt.Sprintf("Invalid transport layer state %v", tsu.state))
			}
		} else if data := event.rxData; data != nil {
			// Received some data. The way we handle it depends on the state:
			//
			// - Authenticating: interpret the data as authentication result,
			//   and if it's successful, then finally set state to Established.
			//   If not, initiate disconnection (and the state will be changed
			//   when we actually disconnect).
			// - Established: pass the data to clients (StreamClient or TradeClient)
			//   via OnRead callback
			// - Any other state: ignore. TODO: also call some error handler to
			//   notify the client.

			switch c.state {
			case ConnStateAuthenticating:
				// Expect authentication result, so try to unmarshal it
				unmarshalAuthnResult := c.paramsInternal.unmarshalAuthnResult
				if unmarshalAuthnResult == nil {
					c.disconnect(errors.Errorf("unmarshalAuthnResult is not set"))
					continue loop
				}

				authnResult, err := unmarshalAuthnResult(data)
				if err != nil {
					c.disconnect(errors.Trace(err))
					continue loop
				}

				if err := c.handleAuthnResult(authnResult); err != nil {
					c.disconnect(errors.Trace(err))
					continue loop
				}

				// Authentication is successful
				// We can now reset any backoff that has been applied
				c.transport.ResetTimeout()

				c.updateState(ConnStateEstablished, nil)

			case ConnStateEstablished:
				// Pass message up to the client
				if c.onReadCB != nil {
					c.onReadCB(data)
				}

			default:
				// We don't expect to receive any messages in this state
				// TODO: report error to the client, but don't disconnect
			}

		} else if al := event.reqAddStateListener; al != nil {
			// Request to add a new state listener.

			sl := stateListener{
				cb:  al.cb,
				opt: al.opt,
			}

			// Determine whether the callback should be called right now
			callNow := al.opt.CallImmediately && (al.state == c.state || al.state == ConnStateAny)

			// Update stored listeners if needed
			if !al.opt.OneOff || !callNow {
				c.stateListeners[al.state] = append(c.stateListeners[al.state], sl)
			}

			if callNow {
				c.callStateListeners(&callStateListenersReq{
					listeners: []stateListener{sl},
					oldState:  c.state,
					state:     c.state,
					cause:     c.stateCause,
				})
			}

			al.result <- struct{}{}
		} else if req := event.reqConnState; req != nil {
			// Request to add a new state listener.
			req.result <- c.state
		} else if req := event.reqSubscribe; req != nil {
			// Request to subscribe
			err := c.subscribeInternal(req.keys)
			req.result <- err
		} else if req := event.reqUnsubscribe; req != nil {
			// Request to unsubscribe
			err := c.unsubscribeInternal(req.keys)
			req.result <- err
		}
	}
}

// NOTE: callStateListeners should only be called from the eventLoop, to ensure
// that all callbacks are only invoked from a single goroutine.
func (c *wsConn) callStateListeners(req *callStateListenersReq) {
	for _, sl := range req.listeners {
		sl.cb(req.oldState, req.state, req.cause)
	}
}

// generateToken creates an access token based on the user's secret access key
func (c *wsConn) generateToken(nonce string) (string, error) {
	secretKeyData, err := base64.StdEncoding.DecodeString(c.params.SecretKey)
	if err != nil {
		return "", errors.Annotatef(err, "base64-decoding the secret key")
	}

	h := hmac.New(sha512.New, secretKeyData)
	payload := fmt.Sprintf("stream_access;access_key_id=%v;nonce=%v;", c.params.APIKey, nonce)
	h.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

func getNonce() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// isClosedConnError is needed because we don't have a separate type for
// that kind of error. Too bad.
func isClosedConnError(err error) bool {
	if err == nil {
		return false
	}

	str := err.Error()
	if strings.Contains(str, "use of closed network connection") {
		return true
	}

	return false
}
