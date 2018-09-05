package streamclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	pbc "code.cryptowat.ch/stream-client-go/proto/client"
	pbm "code.cryptowat.ch/stream-client-go/proto/markets"
	pbs "code.cryptowat.ch/stream-client-go/proto/stream"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

type eventType int

const (
	eventTypeConnOpened eventType = iota
	eventTypeMsg
)

const (
	testApiKey1        = "foo"
	testSecretKey1     = "YmFy"         // base64-encoded "bar"
	testSecretKeyWrong = "YmFyYmFyYmFy" // base64-encoded "barbarbar"
)

var testSubscriptions = []string{"foo", "bar"}

// websocketEvent represents an event like new opened connection or new
// received websocket message
type websocketEvent struct {
	eventType eventType

	// The fields below are only relevant if eventType is eventTypeMsg
	messageType int
	data        []byte
	err         error
}

// websocketTx represents message to send to the websocket
type websocketTx struct {
	messageType int
	data        []byte
	res         chan error
}

// stateTracker {{{
type stateChange struct {
	oldState, state State
	cause           error
}

type stateTracker struct {
	states  []string
	mtx     sync.Mutex
	changes chan stateChange
}

func newStateTracker() *stateTracker {
	return &stateTracker{
		changes: make(chan stateChange, 1024),
	}
}

func (st *stateTracker) AddStateListener(conn *StreamConn, state State, opt StateListenerOpt) {
	conn.AddStateListenerOpt(
		state,
		func(conn *StreamConn, oldState, state State, cause error) {
			st.mtx.Lock()
			defer st.mtx.Unlock()

			errStr := ""
			if cause != nil {
				errStr = fmt.Sprintf("(%s)", cause)
			}

			st.states = append(st.states, fmt.Sprintf("%s->%s%s", StateNames[oldState], StateNames[state], errStr))

			st.changes <- stateChange{
				oldState: oldState,
				state:    state,
				cause:    cause,
			}
		},
		opt,
	)
}

func (st *stateTracker) CheckStates(want []string) error {
	st.mtx.Lock()
	defer st.mtx.Unlock()

	wantStr := strings.Join(want, ", ")
	gotStr := strings.Join(st.states, ", ")

	if gotStr != wantStr {
		return errors.Errorf("states error: want: %q, got: %q", wantStr, gotStr)
	}

	return nil
}

var dontCheckErr = errors.Errorf("_do_not_check_error_")

func (st *stateTracker) ExpectState(state State) error {
	return st.ExpectStateWCause(state, dontCheckErr)
}

func (st *stateTracker) ExpectStateWCause(state State, cause error) error {
	select {
	case change := <-st.changes:
		if change.state != state {
			return errors.Errorf("expect state change: want: %s, got: %s (%v)", StateNames[state], StateNames[change.state], change)
		}

		if cause != dontCheckErr && errors.Cause(change.cause) != cause {
			return errors.Errorf("expect state cause: want: %s, got: %s (%v)", cause, change.cause, change)
		}

	case <-time.After(2 * time.Second):
		return errors.Errorf("expect state change: want: %s, but nothing happened", StateNames[state])
	}

	return nil
}

// }}}

// getRootHandler returns an http handler which upgrades the connection to
// websocket, forwards events (opened connections and received messages) to the
// rx channel, and forwards messages from tx channel to websocket.
//
// NOTE that only one connection should be opened at a time, since currently
// there's no way to receive/send stuff from/to a particular connection in case
// there are many.
func getRootHandler(
	t *testing.T,
	rx chan<- websocketEvent,
	tx <-chan websocketTx,
) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(context.Background())
		upgrader := websocket.Upgrader{}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.Close()

		t.Logf("new websocket conn is opened")

		rx <- websocketEvent{
			eventType: eventTypeConnOpened,
		}

		go func() {
			for {
				mt, message, err := ws.ReadMessage()
				var unmarshalled pbs.StreamMessage
				unmarshalledStr := ""

				if err := proto.Unmarshal(message, &unmarshalled); err != nil {
					unmarshalledStr = fmt.Sprintf("failed to unmarshal: %s", err)
				} else {
					unmarshalledStr = proto.CompactTextString(&unmarshalled)
				}

				t.Logf("websocket rx: type=%d, data=%+v (%s), err=%v", mt, message, unmarshalledStr, err)
				rx <- websocketEvent{
					eventType: eventTypeMsg,

					messageType: mt,
					data:        message,
					err:         err,
				}

				if err != nil {
					t.Logf("breaking out of Rx loop")
					// Signal tx loop to exit as well
					cancel()
					break
				}
			}
		}()

	txLoop:
		for {
			select {
			case msg := <-tx:
				var unmarshalled pbs.StreamMessage
				unmarshalledStr := ""

				if err := proto.Unmarshal(msg.data, &unmarshalled); err != nil {
					unmarshalledStr = fmt.Sprintf("failed to unmarshal: %s", err)
				} else {
					unmarshalledStr = proto.CompactTextString(&unmarshalled)
				}

				t.Logf("websocket tx: type=%d, data=%+v (%s)", msg.messageType, msg.data, unmarshalledStr)

				if err := ws.WriteMessage(msg.messageType, msg.data); err != nil {
					t.Logf("error writing to websocket: %s", err)
					break
				}
			case <-ctx.Done():
				t.Logf("breaking out of Tx loop")
				break txLoop
			}

		}
	}
}

type testServerParams struct {
	rx  <-chan websocketEvent
	tx  chan<- websocketTx
	url string
}

func withTestServer(
	t *testing.T,
	cb func(tp *testServerParams) error,
) error {
	// tx and rx are channels to communicate raw websocket messages with the
	// test server: everything received by the server will be delivered to rx,
	// and everything sent to tx will be sent by the server to the client.
	rx := make(chan websocketEvent, 128)
	tx := make(chan websocketTx, 128)

	// Create test server with a single root endpoint which upgrades connection
	// to websocket
	ts := httptest.NewServer(http.HandlerFunc(getRootHandler(t, rx, tx)))
	defer ts.Close()

	// Replace the scheme in URL to "ws"
	u, err := url.Parse(ts.URL)
	if err != nil {
		return errors.Trace(err)
	}
	u.Scheme = "ws"

	if err := cb(&testServerParams{
		rx:  rx,
		tx:  tx,
		url: u.String(),
	}); err != nil {
		return errors.Trace(err)
	}

	return nil
}

// TestWriteToNonConnected ensures that sending to a non-established StreamConn
// results in an error
func TestWriteToNonConnected(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		conn, err := NewStreamConn(&StreamParams{
			URL: tp.url,
		})
		if err != nil {
			return errors.Trace(err)
		}

		subErr := conn.Subscribe([]string{"foo"})
		if want, got := ErrNotConnected, errors.Cause(subErr); got != want {
			return errors.Errorf("want: %v, got: %v", want, got)
		}

		return nil
	})
	if err != nil {
		t.Error(err)
		return
	}
}

// TestConnectConnected ensures that calling Connect on a connection with
// active connection loop results in an error
func TestConnectConnected(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		c, err := NewStreamConn(&StreamParams{
			URL: tp.url,
		})
		if err != nil {
			return errors.Trace(err)
		}

		if err := c.Connect(); err != nil {
			return errors.Trace(err)
		}

		c2err := c.Connect()
		if want, got := ErrConnLoopActive, errors.Cause(c2err); got != want {
			return errors.Errorf("want: %v, got: %v", want, got)
		}

		return nil
	})
	if err != nil {
		t.Error(err)
		return
	}
}

// TestConnectConnected ensures that calling Connect on a connection with
// active connection loop results in an error
func TestCloseClosed(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		c, err := NewStreamConn(&StreamParams{
			URL: tp.url,
		})
		if err != nil {
			return errors.Trace(err)
		}

		errClose := c.Close()
		if want, got := ErrNotConnected, errors.Cause(errClose); got != want {
			return errors.Errorf("want: %v, got: %v", want, got)
		}

		return nil
	})
	if err != nil {
		t.Error(err)
		return
	}
}

func TestStreamConn(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		// marketRx is a channel to which all MarketUpdateMessage's received by
		// the client will be delivered.
		marketRx := make(chan *pbm.MarketUpdateMessage, 128)

		conn, err := NewStreamConn(&StreamParams{
			URL:           tp.url,
			Subscriptions: testSubscriptions,

			APIKey:    testApiKey1,
			SecretKey: testSecretKey1,
		})
		if err != nil {
			return errors.Trace(err)
		}

		// Add state tracker to the connection, so we'll see all state transitions
		st := newStateTracker()
		st.AddStateListener(conn, StateAny, StateListenerOpt{})

		conn.AddMarketListener(
			func(conn *StreamConn, msg *pbm.MarketUpdateMessage) {
				marketRx <- msg
			},
		)

		if err := conn.Connect(); err != nil {
			return errors.Trace(err)
		}

		if err := st.ExpectState(StateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st.ExpectState(StateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st.ExpectState(StateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Subscribe to one more topic
		if err := conn.Subscribe([]string{"baz"}); err != nil {
			return errors.Errorf("subscribing to baz: %s", err)
		}

		// Wait for the subscribe-to-baz message
		if err := waitSubscribeMsg(t, tp, []string{"baz"}); err != nil {
			return errors.Errorf("waiting for subscribe message: %s", err)
		}

		// Check states so far
		if err := st.CheckStates([]string{
			"disconnected->connecting",
			"connecting->authenticating",
			"authenticating->established",
		}); err != nil {
			return errors.Trace(err)
		}

		// Make sure marketRx is empty
		select {
		case <-marketRx:
			return errors.Errorf("marketRx should be empty")
		default:
			// All right, emtpy.
		}

		// Send heartbeat (should be ignored by the client)
		tp.tx <- websocketTx{
			messageType: websocket.BinaryMessage,
			data:        []byte{1},
		}

		// Send MarketUpdateMessage to the client {{{
		mm := &pbs.StreamMessage{
			Body: &pbs.StreamMessage_MarketUpdate{
				MarketUpdate: &pbm.MarketUpdateMessage{
					Update: &pbm.MarketUpdateMessage_TradesUpdate{
						TradesUpdate: &pbm.TradesUpdate{
							Trades: []*pbm.Trade{
								&pbm.Trade{
									Id:     1,
									Price:  2,
									Amount: 3,
								},
							},
						},
					},
				},
			},
		}

		data, err := proto.Marshal(mm)
		if err != nil {
			return errors.Trace(err)
		}

		tp.tx <- websocketTx{
			messageType: websocket.BinaryMessage,
			data:        data,
		}
		// }}}

		// Wait for MarketUpdateMessage {{{
		if err := func() error {
			select {
			case mm := <-marketRx:
				tu := mm.GetTradesUpdate()
				if tu == nil {
					return errors.Errorf("received something other than TradesUpdate")
				}

				// Check message contents
				if want, got := int64(1), tu.Trades[0].Id; want != got {
					return errors.Errorf("Id: want: %v, got: %v", want, got)
				}

				if want, got := float32(2), tu.Trades[0].Price; want != got {
					return errors.Errorf("Price: want: %v, got: %v", want, got)
				}

				if want, got := float32(3), tu.Trades[0].Amount; want != got {
					return errors.Errorf("Amount: want: %v, got: %v", want, got)
				}

			case <-time.After(1 * time.Second):
				return errors.Errorf("didn't receive anything")
			}

			return nil
		}(); err != nil {
			return errors.Errorf("waiting for MarketUpdateMessage: %s", err)
		}
		// }}}

		// Send garbage, which should result in a reconnection
		tp.tx <- websocketTx{
			messageType: websocket.BinaryMessage,
			data:        []byte{1, 2, 3},
		}

		// Wait for the connection being closed
		if err := waitConnClose(t, tp); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		if err := st.ExpectState(StateWaitBeforeReconnect); err != nil {
			return errors.Trace(err)
		}

		if err := st.ExpectState(StateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st.ExpectState(StateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st.ExpectState(StateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Check states so far
		if err := st.CheckStates([]string{
			"disconnected->connecting",
			"connecting->authenticating",
			"authenticating->established",
			"established->wait_before_reconnect(websocket: close 1003 (unsupported data))",
			"wait_before_reconnect->connecting",
			"connecting->authenticating",
			"authenticating->established",
		}); err != nil {
			return errors.Trace(err)
		}

		return nil
	})
	if err != nil {
		t.Log(errors.ErrorStack(err))
		t.Error(err)
		return
	}
}

func TestAuthnErrors(t *testing.T) {
	return

	err := withTestServer(t, func(tp *testServerParams) error {
		// marketRx is a channel to which all MarketUpdateMessage's received by
		// the client will be delivered.
		marketRx := make(chan *pbm.MarketUpdateMessage, 128)

		conn, err := NewStreamConn(&StreamParams{
			URL:           tp.url,
			Subscriptions: testSubscriptions,

			APIKey:    testApiKey1,
			SecretKey: testSecretKeyWrong,
		})
		if err != nil {
			return errors.Trace(err)
		}

		// Add state tracker to the connection, so we'll see all state transitions
		st := newStateTracker()
		st.AddStateListener(conn, StateAny, StateListenerOpt{})

		conn.AddMarketListener(
			func(conn *StreamConn, msg *pbm.MarketUpdateMessage) {
				marketRx <- msg
			},
		)

		if err := conn.Connect(); err != nil {
			return errors.Trace(err)
		}

		type testCase struct {
			returnedStatus pbs.AuthenticationResult_Status
			retry          int
			expectedCause  error
		}

		testCases := []testCase{
			testCase{pbs.AuthenticationResult_BAD_TOKEN, 1, ErrBadCredentials},
			testCase{pbs.AuthenticationResult_BAD_NONCE, 1, ErrBadNonce},
			testCase{pbs.AuthenticationResult_UNKNOWN, 1, ErrUnknownAuthnError},
			testCase{pbs.AuthenticationResult_TOKEN_EXPIRED, 3, ErrTokenExpired},
		}

		for _, tc := range testCases {
			if err := st.ExpectState(StateConnecting); err != nil {
				return errors.Trace(err)
			}

			// Wait for the new conn to be opened
			if err := waitConnOpen(t, tp); err != nil {
				return errors.Errorf("waiting for new conn to be opened: %s", err)
			}

			if err := st.ExpectState(StateAuthenticating); err != nil {
				return errors.Trace(err)
			}

			// Try to authenticate a certain number of times
			for i := 0; i < tc.retry; i++ {
				// Wait for the authentication request
				if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err == nil {
					return errors.Errorf("authn request should be wrong")
				}

				// Send AuthenticationResult to the client
				if err := sendAuthnResp(t, tp, tc.returnedStatus); err != nil {
					return errors.Errorf("sending authn resp: %s", err)
				}
			}

			// Wait for the conn to be closed
			if err := waitConnClose(t, tp); err != nil {
				return errors.Errorf("waiting for connection being closed: %s", err)
			}

			if err := st.ExpectStateWCause(StateWaitBeforeReconnect, tc.expectedCause); err != nil {
				return errors.Trace(err)
			}
		}

		// Now, finally connect successfully {{{
		if err := st.ExpectState(StateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st.ExpectState(StateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err == nil {
			return errors.Errorf("authn request should be wrong")
		}

		// Send AuthenticationResult to the client
		if err := sendAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}
		// }}}

		return nil
	})
	if err != nil {
		t.Log(errors.ErrorStack(err))
		t.Error(err)
		return
	}
}

func TestStateListeners(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		c, err := NewStreamConn(&StreamParams{
			URL: tp.url,

			APIKey:        testApiKey1,
			SecretKey:     testSecretKey1,
			Subscriptions: testSubscriptions,
		})
		if err != nil {
			return errors.Trace(err)
		}

		type testCase struct {
			state                   State
			oneOff, callImmediately bool
			wantTransitions         []string
		}

		// Init test cases table {{{
		testCases := []testCase{
			testCase{
				state: StateAny, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"disconnected->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->wait_before_reconnect(websocket: close 1005 (no status))",
					"wait_before_reconnect->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->disconnected(websocket: close 1000 (normal))",
					"disconnected->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateAny, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
					"disconnected->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->wait_before_reconnect(websocket: close 1005 (no status))",
					"wait_before_reconnect->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->disconnected(websocket: close 1000 (normal))",
					"disconnected->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateAny, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"disconnected->connecting",
				},
			},
			testCase{
				state: StateAny, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
				},
			},

			testCase{
				state: StateEstablished, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"authenticating->established",
					"authenticating->established",
					"authenticating->established",
				},
			},
			testCase{
				state: StateEstablished, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"authenticating->established",
					"authenticating->established",
					"authenticating->established",
				},
			},
			testCase{
				state: StateEstablished, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"authenticating->established",
				},
			},
			testCase{
				state: StateEstablished, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"authenticating->established",
				},
			},

			testCase{
				state: StateDisconnected, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"established->disconnected(websocket: close 1000 (normal))",
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateDisconnected, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
					"established->disconnected(websocket: close 1000 (normal))",
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateDisconnected, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateDisconnected, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
				},
			},

			testCase{
				state: StateWaitBeforeReconnect, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"established->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: StateWaitBeforeReconnect, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"established->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: StateWaitBeforeReconnect, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"established->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: StateWaitBeforeReconnect, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"established->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
		}
		// }}}

		// Create state trackers for each test case
		st := make([]*stateTracker, len(testCases))
		for i, v := range testCases {
			st[i] = newStateTracker()
			st[i].AddStateListener(c, v.state, StateListenerOpt{
				OneOff: v.oneOff, CallImmediately: v.callImmediately,
			})
		}

		if err := c.Connect(); err != nil {
			return errors.Trace(err)
		}

		if err := st[0].ExpectState(StateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st[0].ExpectState(StateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st[0].ExpectState(StateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Reconnect
		c.transport.CloseOpt(nil, false)

		// Wait for the connection being closed
		if err := waitConnClose(t, tp); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		if err := st[0].ExpectState(StateWaitBeforeReconnect); err != nil {
			return errors.Trace(err)
		}

		if err := st[0].ExpectState(StateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st[0].ExpectState(StateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st[0].ExpectState(StateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Close and stop reconnecting
		if err := c.Close(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the connection being closed
		if err := waitConnClose(t, tp); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		if err := st[0].ExpectState(StateDisconnected); err != nil {
			return errors.Trace(err)
		}

		// Connect again
		if err := c.Connect(); err != nil {
			return errors.Trace(err)
		}

		if err := st[0].ExpectState(StateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st[0].ExpectState(StateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st[0].ExpectState(StateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Close and stop reconnecting
		if err := c.Close(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the connection being closed
		if err := waitConnClose(t, tp); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		if err := st[0].ExpectState(StateDisconnected); err != nil {
			return errors.Trace(err)
		}

		// Check states from all test cases

		for i, v := range testCases {
			if err := st[i].CheckStates(v.wantTransitions); err != nil {
				return errors.Annotatef(err, "test case #%d", i)
			}
		}

		return nil
	})
	if err != nil {
		t.Log(errors.ErrorStack(err))
		t.Error(err)
		return
	}
}

func TestSubscribe(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		conn, err := NewStreamConn(&StreamParams{
			URL:           tp.url,
			APIKey:        testApiKey1,
			SecretKey:     testSecretKey1,
			Subscriptions: []string{"foo", "bar"},
		})
		if err != nil {
			return errors.Trace(err)
		}

		if err := conn.Connect(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Subscribe to new sub keys
		if err := conn.Subscribe([]string{"baz", "woo"}); err != nil {
			return errors.Trace(err)
		}

		have := conn.GetSubscriptions()
		want := []string{"foo", "bar", "baz", "woo"}
		sort.Strings(have)
		sort.Strings(want)

		if !reflect.DeepEqual(want, have) {
			return errors.Errorf("SubscriptionKeys: want: %+v have: %v", want, have)
		}

		return nil
	})

	if err != nil {
		t.Error(err)
		return
	}
}

func TestDefaultURL(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		conn, err := NewStreamConn(&StreamParams{})
		if err != nil {
			return errors.Trace(err)
		}

		if want, got := "wss://stream.cryptowat.ch", conn.URL(); got != want {
			return errors.Errorf("want: %v, got: %v", want, got)
		}

		return nil
	})
	if err != nil {
		t.Error(err)
		return
	}
}

func TestDefaultOptions(t *testing.T) {
	err := withTestServer(t, func(tp *testServerParams) error {
		conn, err := NewStreamConn(&StreamParams{})
		if err != nil {
			return errors.Trace(err)
		}

		// If the reconnect options aren't the defaults, something is wrong
		if !(conn.params.ReconnectOpts.Reconnect == defaultReconnectOpts.Reconnect &&
			conn.params.ReconnectOpts.Backoff == defaultReconnectOpts.Backoff &&
			conn.params.ReconnectOpts.ReconnectTimeout == defaultReconnectOpts.ReconnectTimeout &&
			conn.params.ReconnectOpts.MaxReconnectTimeout == defaultReconnectOpts.MaxReconnectTimeout) {
			return errors.New("default parameters not set properly")
		}

		return nil
	})
	if err != nil {
		t.Error(err)
		return
	}
}

func waitConnOpen(t *testing.T, tp *testServerParams) error {
	select {
	case event := <-tp.rx:
		if want, got := eventTypeConnOpened, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v (%+v)", want, got, event)
		}

	case <-time.After(1 * time.Second):
		return errors.Errorf("didn't receive anything")
	}

	return nil
}

func waitAuthnReq(t *testing.T, tp *testServerParams, apiKey, secretKey string) error {
	select {
	case event := <-tp.rx:
		if want, got := eventTypeMsg, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v", want, got)
		}

		var cm pbc.ClientMessage
		if err := proto.Unmarshal(event.data, &cm); err != nil {
			return errors.Trace(err)
		}

		apiAuthn := cm.GetApiAuthentication()
		if apiAuthn == nil {
			return errors.Errorf("received something other than api authentication")
		}

		if got, want := apiAuthn.ApiKey, apiKey; got != want {
			return errors.Errorf("ApiKey: want: %q, got: %q", want, got)
		}

		token, err := generateToken(apiKey, secretKey, apiAuthn.Nonce)
		if err != nil {
			return errors.Trace(err)
		}

		version := apiAuthn.GetVersion()
		if version == "" {
			return errors.Errorf("Client version not set in auth message")
		}

		if !hmac.Equal([]byte(apiAuthn.Token), []byte(token)) {
			return errors.Errorf("HMAC: want: % x, got: % x", token, apiAuthn.Token)
		}

		// TODO: when nonce is opaque to client, check here that nonce is correct

	case <-time.After(1 * time.Second):
		return errors.Errorf("didn't receive anything")
	}

	return nil
}

func sendAuthnResp(t *testing.T, tp *testServerParams, status pbs.AuthenticationResult_Status) error {
	t.Logf("Sending authn response, status: %v", status)
	sm := &pbs.StreamMessage{
		Body: &pbs.StreamMessage_AuthenticationResult{
			AuthenticationResult: &pbs.AuthenticationResult{
				Status: status,
			},
		},
	}

	data, err := proto.Marshal(sm)
	if err != nil {
		return errors.Trace(err)
	}

	tp.tx <- websocketTx{
		messageType: websocket.BinaryMessage,
		data:        data,
	}

	return nil
}

func waitSubscribeMsg(t *testing.T, tp *testServerParams, subs []string) error {
	select {
	case event := <-tp.rx:
		if want, got := eventTypeMsg, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v", want, got)
		}

		var cm pbc.ClientMessage
		if err := proto.Unmarshal(event.data, &cm); err != nil {
			return errors.Trace(err)
		}

		cs := cm.GetSubscribe()
		if cs == nil {
			return errors.Errorf("received something other than subscribe")
		}

		// Check subscription keys
		{
			want := subs
			if !reflect.DeepEqual(want, cs.SubscriptionKeys) {
				return errors.Errorf("SubscriptionKeys: want: %+v, got: %+v", want, cs.SubscriptionKeys)
			}
		}

	case <-time.After(1 * time.Second):
		return errors.Errorf("didn't receive anything")
	}

	return nil
}

func waitConnClose(t *testing.T, tp *testServerParams) error {
	select {
	case event := <-tp.rx:
		if want, got := eventTypeMsg, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v (%+v)", want, got, event)
		}

		if event.err == nil {
			return errors.Errorf("event.err should not be nil")
		}

	case <-time.After(1 * time.Second):
		return errors.Errorf("didn't receive anything")
	}

	return nil
}

func generateToken(apiKey, secretKey, nonce string) (string, error) {
	secretKeyData, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil {
		return "", errors.Annotatef(err, "base64-decoding the secret key")
	}

	h := hmac.New(sha512.New, secretKeyData)
	payload := fmt.Sprintf("stream_access;access_key_id=%v;nonce=%v;", apiKey, nonce)
	h.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
