package wsclient // import "code.cryptowat.ch/ws-client-go"

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
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

	// How many times to retry authentication in case of expired token
	authnExpiredTokenRetryCnt = 3
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
	params        WSParams
	subscriptions map[string]struct{}

	transport *internal.StreamTransportConn

	callStateListeners chan callStateListenersReq

	authnResCh chan error

	authnCtx       context.Context
	authnCtxCancel context.CancelFunc

	// Current state
	state ConnState

	// Error caused the current state; only relevant for TransportStateDisconnected and
	// TransportStateWaitBeforeReconnect, for other states it's always nil.
	stateCause error

	// TODO: document
	isExpectedCause func(cause error) bool
	actualCause     error

	stateListeners map[ConnState][]stateListener

	mtx sync.Mutex
}

// newWsConn creates a new websocket connection with the given params.
//
// Note that clients should manually call Connect on a newly created
// connection; the rationale is that clients might register some state and/or
// message handlers before the connection, to avoid any possible races.
func newWsConn(params *WSParams) (*wsConn, error) {
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
		params:        p,
		subscriptions: make(map[string]struct{}),

		transport: transport,

		callStateListeners: make(chan callStateListenersReq, 1),

		stateListeners: make(map[ConnState][]stateListener),
	}

	for _, sub := range params.Subscriptions {
		c.subscriptions[sub] = struct{}{}
	}

	transport.OnStateChange(
		func(_ *internal.StreamTransportConn, oldTransportState, transportState internal.TransportState, cause error) {

			switch transportState {

			case
				internal.TransportStateDisconnected,
				internal.TransportStateWaitBeforeReconnect,
				internal.TransportStateConnecting:

				var state ConnState
				switch transportState {
				case internal.TransportStateDisconnected:
					state = ConnStateDisconnected
				case internal.TransportStateWaitBeforeReconnect:
					state = ConnStateWaitBeforeReconnect
				case internal.TransportStateConnecting:
					state = ConnStateConnecting
				default:
					// Should never be here
					panic(fmt.Sprintf("unexpected transport state: %d", transportState))
				}

				c.mtx.Lock()
				// See if we need to replace the cause returned by transport layer
				// with another (more specific) one
				if c.isExpectedCause != nil && c.isExpectedCause(cause) {
					cause = c.actualCause
				}

				c.isExpectedCause = nil
				c.actualCause = nil

				c.updateState(state, errors.Trace(cause))
				c.mtx.Unlock()

			case internal.TransportStateConnected:
				c.mtx.Lock()
				c.updateState(ConnStateAuthenticating, errors.Trace(cause))
				authnResCh := c.authnResCh
				ctx := c.authnCtx
				c.mtx.Unlock()

				go func() (authnErr error) {
					defer func() {
						if authnErr != nil {
							// Failed to perform authentication; if the state is still
							// "authenticating", disconnect
							c.mtx.Lock()
							defer c.mtx.Unlock()

							if c.state == ConnStateAuthenticating {
								// And also substitute the generic websocket closing error
								// with the specific error (authnErr)
								c.substituteUpcomingCause(func(cause error) bool {
									cause = errors.Cause(cause)
									return websocket.IsCloseError(cause, websocket.CloseProtocolError) ||
										isClosedConnError(cause)
								}, authnErr)

								c.transport.CloseOpt(
									websocket.FormatCloseMessage(websocket.CloseProtocolError, ""),
									false,
								)
							}
						}
					}()

					// Authenticate {{{
					var authnRes error
				authnLoop:
					for i := 0; i < authnExpiredTokenRetryCnt; i++ {
						nonce := getNonce()
						token, err := c.generateToken(nonce)
						if err != nil {
							return errors.Trace(err)
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

						select {
						case <-ctx.Done():
							return errors.Trace(ctx.Err())
						default:
						}

						if err := c.sendProto(ctx, authMsg); err != nil {
							return errors.Trace(err)
						}

						select {
						case <-ctx.Done():
							return errors.Trace(ctx.Err())
						case authnRes = <-authnResCh:
							if authnRes != nil {
								if errors.Cause(authnRes) == ErrTokenExpired {
									// Token is expired: try to authn again (if not tried too
									// many times already)
									continue authnLoop
								}
								return errors.Trace(authnRes)
							}
						}

						break authnLoop
					}
					if authnRes != nil {
						return errors.Trace(authnRes)
					}
					// }}}

					select {
					case <-ctx.Done():
						return errors.Trace(ctx.Err())
					default:
					}

					// NOTE: as we switch state to ConnStateEstablished, ctx will be
					// cancelled, so we shouldn't check it anymore and just return nil
					c.mtx.Lock()
					c.updateState(ConnStateEstablished, errors.Trace(cause))
					c.mtx.Unlock()
					return nil
				}()
			}

		},
	)

	// Start goroutine which will call state listeners
	go c.stateListenerLoop()

	return c, nil
}

// Connect either starts a connection goroutine (if state is
// ConnStateDisconnected), or makes it to stop waiting a timeout and connect right
// now (if state is ConnStateWaitBeforeReconnect). For other states, returns an
// error.
//
// It doesn't wait for the connection to establish, and returns immediately.
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

func (c *wsConn) authHandler(result *pbs.AuthenticationResult) {
	if result != nil {
		// Got authentication result message
		var authnRes error

		switch result.Status {
		case pbs.AuthenticationResult_AUTHENTICATED:
			// We can now reset any backoff that has been applied
			c.transport.ResetTimeout()

		case pbs.AuthenticationResult_TOKEN_EXPIRED:
			// The token is expired, we'll probably try authenticating again
			authnRes = ErrTokenExpired

		case pbs.AuthenticationResult_BAD_TOKEN:
			// User provided bad creds
			c.transport.CloseOpt(
				websocket.FormatCloseMessage(websocket.CloseProtocolError, ""),
				false,
			)
			authnRes = ErrBadCredentials

		case pbs.AuthenticationResult_BAD_NONCE:
			c.transport.CloseOpt(
				websocket.FormatCloseMessage(websocket.CloseProtocolError, ""),
				false,
			)
			authnRes = ErrBadNonce

		case pbs.AuthenticationResult_UNKNOWN:
			// Shouldn't happen normally; it can happen e.g. if stream has failed
			// to connect to biller, or failed to call biller's methods.
			c.transport.CloseOpt(
				websocket.FormatCloseMessage(websocket.CloseProtocolError, ""),
				false,
			)
			authnRes = ErrUnknownAuthnError
		}

		// If we are still waiting for the result, send it
		c.mtx.Lock()
		authnResCh := c.authnResCh
		c.mtx.Unlock()

		if authnResCh != nil {
			authnResCh <- authnRes
		}
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
	sl := stateListener{
		cb:  cb,
		opt: opt,
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	// Determine whether the callback should be called right now
	callNow := opt.CallImmediately && (state == c.state || state == ConnStateAny)

	// Update stored listeners if needed
	if !opt.OneOff || !callNow {
		c.stateListeners[state] = append(c.stateListeners[state], sl)
	}

	if callNow {
		c.callStateListeners <- callStateListenersReq{
			listeners: []stateListener{sl},
			oldState:  c.state,
			state:     c.state,
			cause:     c.stateCause,
		}
	}
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
// Note that the initial set of subscription keys can be given as params to
// newWsConn. Also note that calling Subscribe() doesn't alter the initial
// keys to subscribe to when the client reconnects; so after the reconnection
// the effect of calling Subscribe is lost.
func (c *wsConn) Subscribe(keys []string) error {
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

// Unsubscribe unsubscribes from the given set of keys. Also see notes for
// Subscribe.
func (c *wsConn) Unsubscribe(keys []string) error {
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

// ConnState returns current client connection state.
func (c *wsConn) ConnState() ConnState {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	return c.state
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
	c.callStateListeners <- callStateListenersReq{
		listeners: listeners,
		oldState:  oldState,
		state:     state,
		cause:     cause,
	}
}

// enterLeaveState should be called on leaving and entering each state. So,
// when changing state from A to B, it's called twice, like this:
//
//      enterLeaveState(A, false)
//      enterLeaveState(B, true)
func (c *wsConn) enterLeaveState(state ConnState, enter bool) {
	switch state {

	case ConnStateAuthenticating:
		// authnCtx and its cancel func should be present in all states but
		// TransportStateDisconnected
		if enter {
			c.authnCtx, c.authnCtxCancel = context.WithCancel(context.Background())
			c.authnResCh = make(chan error, 1)
		} else {
			c.authnCtxCancel()

			c.authnCtx = nil
			c.authnCtxCancel = nil
			c.authnResCh = nil
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

func (c *wsConn) stateListenerLoop() {
	for {
		req := <-c.callStateListeners
		for _, sl := range req.listeners {
			sl.cb(req.oldState, req.state, req.cause)
		}
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

// substituteUpcomingCause should be used when we're expecting the state to
// change very soon (e.g. when closing a connection because of authentication
// error), so it takes a predicate which checks the causing error returned from
// the transport layer, and also the actual error. If predicate returns true
// for the next state change cause, the actual one will be given to client's
// listeners.
func (c *wsConn) substituteUpcomingCause(
	isExpected func(cause error) bool,
	actual error,
) {
	c.isExpectedCause = isExpected
	c.actualCause = actual
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
