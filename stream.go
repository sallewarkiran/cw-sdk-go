package streamclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	pbc "github.com/cryptowatch/proto/client"
	pbm "github.com/cryptowatch/proto/markets"
	pbs "github.com/cryptowatch/proto/stream"
	"github.com/cryptowatch/stream-client-go/internal"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

const (
	defaultURL = "wss://stream.cryptowat.ch"

	// How many times to retry authentication in case of expired token
	authnExpiredTokenRetryCnt = 3
)

var (
	// StateNames contains human-readable names for connection states.
	StateNames = make([]string, StatesCnt)

	// ErrNotConnected means the connection is not established when the client
	// tried to e.g. send a message, or close the connection.
	ErrNotConnected = errors.New("not connected")

	// ErrConnLoopActive means the client tried to connect when the connection
	// loop is already active.
	ErrConnLoopActive = errors.New("connection loop is already active")

	// ErrBadCredentials means the provided APIKey and/or SecretKey were invalid.
	ErrBadCredentials = errors.New("bad credentials")

	// ErrTokenExpired means the authentication procedure took too long and the
	// token was already expired. The library tries to retry the authentication
	// 3 times before actually passing that error to the client.
	ErrTokenExpired = errors.New("token is expired")

	// ErrBadNonce means the nonce used in the authentication request is not
	// larger than the last used nonce.
	ErrBadNonce = errors.New("bad nonce")

	// ErrUnknownAuthnError means some unexpected authentication problem;
	// possibly caused by an internal error on stream server.
	ErrUnknownAuthnError = errors.New("unknown authentication error")
)

func init() {
	StateNames[StateDisconnected] = "disconnected"
	StateNames[StateWaitBeforeReconnect] = "wait_before_reconnect"
	StateNames[StateConnecting] = "connecting"
	StateNames[StateAuthenticating] = "authenticating"
	StateNames[StateEstablished] = "established"
}

// StreamParams contains params for opening a client stream connection
type StreamParams struct {
	// Server URL, e.g. wss://sb.cryptowat.ch
	URL string

	// APIKey and SecretKey are stream credentials
	APIKey    string
	SecretKey string

	// Initial set of subscription keys. Client will automatically subscribe to
	// those every time it's connected or reconnected.
	Subscriptions []string

	// Whether the library should reconnect automatically
	Reconnect bool
	// Reconnection backoff: if true, then the reconnection time will be
	// initially ReconnectTimeout, then will become twice as long each
	// unsuccessful connection attempt; but it won't be longer than
	// MaxReconnectTimeout.
	Backoff bool
	// Initial reconnection timeout: if zero, then 1 second will be used
	ReconnectTimeout time.Duration
	// Max reconnect timeout. If zero, then 30 seconds will be used.
	MaxReconnectTimeout time.Duration
}

type State int

const (
	// StateDisconnected means we're disconnected and not trying to connect.
	// connLoop is not running.
	StateDisconnected State = iota

	// StateWaitBeforeReconnect means we already tried to connect, but then
	// either the connection failed, or succeeded but later disconnected for some
	// reason (see stateCause), and now we're waiting for a timeout before
	// connecting again. wsConn is nil, but connCtx and connCtxCancel are not,
	// and connLoop is running.
	StateWaitBeforeReconnect

	// StateConnecting means we're calling websocket.DefaultDialer.Dial() right
	// now.
	StateConnecting

	// StateAuthenticating means the transport (websocket) connection is
	// established, and right now we're exchanging the authentication and
	// identification messages
	StateAuthenticating

	// StateEstablished means the connection is ready
	StateEstablished

	// StatesCnt is a number of states
	StatesCnt

	// StateAny can be used with AddStateListener() and AddStateListenerOpt()
	// in order to listen for all states.
	StateAny = -1
)

// StreamConn represents a stream connection, it contains unexported fields
type StreamConn struct {
	params StreamParams

	transport *internal.StreamTransportConn

	callStateListeners  chan callStateListenersReq
	callMarketListeners chan callMarketListenersReq
	callPairListeners   chan callPairListenersReq

	authnResCh chan error

	authnCtx       context.Context
	authnCtxCancel context.CancelFunc

	// Current state
	state State
	// Error caused the current state; only relevant for TransportStateDisconnected and
	// TransportStateWaitBeforeReconnect, for other states it's always nil.
	stateCause error

	// TODO: document
	isExpectedCause func(cause error) bool
	actualCause     error

	stateListeners  map[State][]stateListener
	marketListeners []OnMarketUpdateCallback
	pairListeners   []OnPairUpdateCallback

	mtx sync.Mutex
}

// NewStreamConn creates a new stream connection with the given params.
//
// Note that clients should manually call Connect on a newly created
// connection; the rationale is that clients might register some state and/or
// message handlers before the connection, to avoid any possible races.
func NewStreamConn(params *StreamParams) (*StreamConn, error) {
	p := *params

	if p.URL == "" {
		p.URL = defaultURL
	}

	transport, err := internal.NewStreamTransportConn(&internal.StreamTransportParams{
		URL:                 p.URL,
		Reconnect:           p.Reconnect,
		Backoff:             p.Backoff,
		ReconnectTimeout:    p.ReconnectTimeout,
		MaxReconnectTimeout: p.MaxReconnectTimeout,
	})
	if err != nil {
		return nil, errors.Trace(err)
	}

	c := &StreamConn{
		params: p,

		transport: transport,

		callStateListeners:  make(chan callStateListenersReq, 1),
		callMarketListeners: make(chan callMarketListenersReq, 1),
		callPairListeners:   make(chan callPairListenersReq, 1),

		stateListeners: make(map[State][]stateListener),
	}

	transport.OnStateChange(
		func(_ *internal.StreamTransportConn, oldTransportState, transportState internal.TransportState, cause error) {

			switch transportState {

			case
				internal.TransportStateDisconnected,
				internal.TransportStateWaitBeforeReconnect,
				internal.TransportStateConnecting:

				var state State
				switch transportState {
				case internal.TransportStateDisconnected:
					state = StateDisconnected
				case internal.TransportStateWaitBeforeReconnect:
					state = StateWaitBeforeReconnect
				case internal.TransportStateConnecting:
					state = StateConnecting
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
				c.updateState(StateAuthenticating, errors.Trace(cause))
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

							if c.state == StateAuthenticating {
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
									Token:  token,
									Nonce:  nonce,
									ApiKey: c.params.APIKey,
									Source: pbc.APIAuthenticationMessage_GOLANG_SDK,
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

					// Send client ID {{{
					cm := &pbc.ClientMessage{
						Body: &pbc.ClientMessage_Identification{
							Identification: &pbc.ClientIdentificationMessage{
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

					if err := c.sendProto(ctx, cm); err != nil {
						return errors.Trace(err)
					}
					// }}}

					select {
					case <-ctx.Done():
						return errors.Trace(ctx.Err())
					default:
					}

					// NOTE: as we switch state to StateEstablished, ctx will be
					// cancelled, so we shouldn't check it anymore and just return nil
					c.mtx.Lock()
					c.updateState(StateEstablished, errors.Trace(cause))
					c.mtx.Unlock()
					return nil
				}()
			}

		},
	)

	transport.OnRead(func(sc *internal.StreamTransportConn, data []byte) {
		var msg pbs.StreamMessage

		if err := proto.Unmarshal(data, &msg); err != nil {
			// Failed to parse incoming message: close connection (and if
			// reconnection was requested, then reconnect)
			c.transport.CloseOpt(
				websocket.FormatCloseMessage(websocket.CloseUnsupportedData, ""),
				false,
			)
			return
		}

		switch msg.Body.(type) {
		case *pbs.StreamMessage_AuthenticationResult:
			c.authHandler(msg.GetAuthenticationResult())
		case *pbs.StreamMessage_MarketUpdate:
			c.marketUpdateHandler(msg.GetMarketUpdate())
		case *pbs.StreamMessage_PairUpdate:
			c.pairUpdateHandler(msg.GetPairUpdate())
		default:
			// not a supported type
		}

	})

	// Start goroutine which will call state listeners. See comment for
	// notifyLoop for the rationale of this design.
	go c.notifyLoop()

	return c, nil
}

// Connect either starts a connection goroutine (if state is
// StateDisconnected), or makes it to stop waiting a timeout and connect right
// now (if state is StateWaitBeforeReconnect). For other states, returns an
// error.
//
// It doesn't wait for the connection to establish, and returns immediately.
func (c *StreamConn) Connect() (err error) {
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
func (c *StreamConn) Close() (err error) {
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

func (c *StreamConn) authHandler(result *pbs.AuthenticationResult) {
	if result != nil {
		// Got authentication result message
		var authnRes error

		switch result.Status {
		case pbs.AuthenticationResult_AUTHENTICATED:
			// Good news. no-op

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

func (c *StreamConn) marketUpdateHandler(update *pbm.MarketUpdateMessage) {
	if update != nil {
		c.mtx.Lock()
		listeners := make([]OnMarketUpdateCallback, len(c.marketListeners))
		for i, v := range c.marketListeners {
			listeners[i] = v
		}
		c.mtx.Unlock()

		// Request listeners invocation (will be invoked by a dedicated goroutine)
		c.callMarketListeners <- callMarketListenersReq{
			listeners: listeners,
			msg:       update,
		}
	}
}

func (c *StreamConn) pairUpdateHandler(update *pbm.PairUpdateMessage) {
	if update != nil {
		c.mtx.Lock()
		listeners := make([]OnPairUpdateCallback, len(c.pairListeners))
		for i, v := range c.pairListeners {
			listeners[i] = v
		}
		c.mtx.Unlock()
		c.callPairListeners <- callPairListenersReq{
			listeners: listeners,
			msg:       update,
		}
	}
}

// StateCallback is a signature of a state listener. Arguments conn, oldState
// and state are self-descriptive; cause is the error which caused the current
// state. Cause is relevant only for StateDisconnected and
// StateWaitBeforeReconnect (in which case it's either the reason of failure to
// connect, or reason of disconnection), for other states it's always nil.
//
// See AddStateListener.
type StateCallback func(conn *StreamConn, oldState, state State, cause error)

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
// To subscribe to all state changes, use StateAny as a state.
func (c *StreamConn) AddStateListener(state State, cb StateCallback) {
	c.AddStateListenerOpt(state, cb, StateListenerOpt{})
}

// AddStateListenerOpt is like AddStateListener, but also takes additional
// options; see StateListenerOpt for details.
func (c *StreamConn) AddStateListenerOpt(state State, cb StateCallback, opt StateListenerOpt) {
	sl := stateListener{
		cb:  cb,
		opt: opt,
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	// Determine whether the callback should be called right now
	callNow := opt.CallImmediately && (state == c.state || state == StateAny)

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

// Subscribe subscribes to the given set of keys. Example:
//
//   conn.Subscribe([]string{
//           "market:bitfinex:btcusd:orderbook:deltas",
//           "market:bitfinex:btceur:orderbook:deltas",
//   })
//
// Note that the initial set of subscription keys can be given as params to
// NewStreamConn. Also note that calling Subscribe() doesn't alter the initial
// keys to subscribe to when the client reconnects; so after the reconnection
// the effect of calling Subscribe is lost.
func (c *StreamConn) Subscribe(keys []string) error {
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

	return nil
}

// Unsubscribe unsubscribes from the given set of keys. Also see notes for
// Subscribe.
func (c *StreamConn) Unsubscribe(keys []string) error {
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

	return nil
}

// OnMarketUpdateCallback is a signature of a market update listener.
type OnMarketUpdateCallback func(conn *StreamConn, msg *pbm.MarketUpdateMessage)
type OnPairUpdateCallback func(conn *StreamConn, msg *pbm.PairUpdateMessage)

// AddMarketListener registers a new listener for all received market update
// messages.
func (c *StreamConn) AddMarketListener(cb OnMarketUpdateCallback) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.marketListeners = append(c.marketListeners, cb)
}

// AddMarketListener registers a new listener for all received market update
// messages.
func (c *StreamConn) AddPairListener(cb OnPairUpdateCallback) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.pairListeners = append(c.pairListeners, cb)
}

// State() returns current connection state.
func (c *StreamConn) State() State {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	return c.state
}

// URL returns an url used for connection.
func (c *StreamConn) URL() string {
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
	oldState, state State
	cause           error
}

type callMarketListenersReq struct {
	msg       *pbm.MarketUpdateMessage
	listeners []OnMarketUpdateCallback
}

type callPairListenersReq struct {
	msg       *pbm.PairUpdateMessage
	listeners []OnPairUpdateCallback
}

func (c *StreamConn) updateState(state State, cause error) {

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
	listeners := append(c.stateListeners[state], c.stateListeners[StateAny]...)

	// Remove one-off listeners
	c.stateListeners[state] = removeOneOff(c.stateListeners[state])
	c.stateListeners[StateAny] = removeOneOff(c.stateListeners[StateAny])

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
func (c *StreamConn) enterLeaveState(state State, enter bool) {
	switch state {

	case StateAuthenticating:
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
func (c *StreamConn) sendProto(ctx context.Context, pb proto.Message) (err error) {
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

// notifyLoop calls state- and message-listeners.
func (c *StreamConn) notifyLoop() {
	for {
		select {
		case req := <-c.callStateListeners:
			for _, sl := range req.listeners {
				sl.cb(c, req.oldState, req.state, req.cause)
			}

		case req := <-c.callMarketListeners:
			for _, l := range req.listeners {
				l(c, req.msg)
			}

		case req := <-c.callPairListeners:
			for _, l := range req.listeners {
				l(c, req.msg)
			}
		}

	}
}

// generateToken creates an access token based on the user's secret access key
func (c *StreamConn) generateToken(nonce string) (string, error) {
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
func (c *StreamConn) substituteUpcomingCause(
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
