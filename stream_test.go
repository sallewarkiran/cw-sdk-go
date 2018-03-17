package streamclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cryptowatch/proto/client"
	"github.com/cryptowatch/proto/markets"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

// stateTracker {{{
type stateTracker struct {
	states []string
	mtx    sync.Mutex
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

				t.Logf("websocket rx: type=%d, data=%+v, err=%v", mt, message, err)
				rx <- websocketEvent{
					eventType: eventTypeMsg,

					messageType: mt,
					data:        message,
					err:         err,
				}

				if err != nil {
					break
				}
			}
		}()

		for {
			msg := <-tx

			t.Logf("websocket tx: type=%d, data=%+v", msg.messageType, msg.data)

			if err := ws.WriteMessage(msg.messageType, msg.data); err != nil {
				t.Logf("error writing to websocket: %s", err)
				break
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
	cb func(tp testServerParams) error,
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

	if err := cb(testServerParams{
		rx:  rx,
		tx:  tx,
		url: u.String(),
	}); err != nil {
		return errors.Trace(err)
	}

	return nil
}

// TestWriteToNonConnected ensures that sending to a non-connected StreamConn
// results in an error
func TestWriteToNonConnected(t *testing.T) {
	err := withTestServer(t, func(tp testServerParams) error {
		conn, err := NewStreamConn(&StreamParams{
			URL:              tp.url,
			Reconnect:        true,
			ReconnectTimeout: 1 * time.Millisecond,
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
	err := withTestServer(t, func(tp testServerParams) error {
		c, err := NewStreamConn(&StreamParams{
			URL:              tp.url,
			Reconnect:        true,
			ReconnectTimeout: 1 * time.Millisecond,
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
	err := withTestServer(t, func(tp testServerParams) error {
		c, err := NewStreamConn(&StreamParams{
			URL:              tp.url,
			Reconnect:        true,
			ReconnectTimeout: 1 * time.Millisecond,
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

func TestMarketConn(t *testing.T) {
	err := withTestServer(t, func(tp testServerParams) error {
		// marketRx is a channel to which all MarketUpdateMessage's received by
		// the client will be delivered.
		marketRx := make(chan *ProtobufMarkets.MarketUpdateMessage, 128)

		conn, err := NewMarketConn(&MarketParams{
			StreamParams: StreamParams{
				URL:              tp.url,
				Reconnect:        true,
				ReconnectTimeout: 1 * time.Millisecond,
				Subscriptions:    []string{"foo", "bar"},
			},
		})
		if err != nil {
			return errors.Trace(err)
		}

		// Add state tracker to the connection, so we'll see all state transitions
		st := stateTracker{}
		st.AddStateListener(conn.StreamConn, StateAny, StateListenerOpt{})

		conn.AddMessageListener(
			func(conn *MarketConn, msg *ProtobufMarkets.MarketUpdateMessage) {
				marketRx <- msg
			},
		)

		if err := conn.Connect(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(tp.rx); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		// Wait for the client identification message
		if err := waitIdMsg(tp.rx, []string{"foo", "bar"}); err != nil {
			return errors.Errorf("waiting for client identification message: %s", err)
		}

		// Subscribe to one more topic
		if err := conn.Subscribe([]string{"baz"}); err != nil {
			return errors.Errorf("subscribing to baz: %s", err)
		}

		// Wait for the subscribe-to-baz message
		if err := waitSubscribeMsg(tp.rx, []string{"baz"}); err != nil {
			return errors.Errorf("waiting for subscribe message: %s", err)
		}

		// Check states so far
		if err := st.CheckStates([]string{
			"disconnected->connecting",
			"connecting->connected",
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
		mm := &ProtobufMarkets.MarketUpdateMessage{
			Update: &ProtobufMarkets.MarketUpdateMessage_TradesUpdate{
				TradesUpdate: &ProtobufMarkets.TradesUpdate{
					Trades: []*ProtobufMarkets.Trade{
						&ProtobufMarkets.Trade{
							Id:     1,
							Price:  2,
							Amount: 3,
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
		if err := waitConnClose(tp.rx); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(tp.rx); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		// Wait for the client identification message
		if err := waitIdMsg(tp.rx, []string{"foo", "bar"}); err != nil {
			return errors.Errorf("waiting for client identification message: %s", err)
		}

		// Check states so far
		if err := st.CheckStates([]string{
			"disconnected->connecting",
			"connecting->connected",
			"connected->wait_before_reconnect(websocket: close 1003 (unsupported data))",
			"wait_before_reconnect->connecting",
			"connecting->connected",
		}); err != nil {
			return errors.Trace(err)
		}

		return nil
	})
	if err != nil {
		t.Error(err)
		return
	}
}

func TestStateListeners(t *testing.T) {
	err := withTestServer(t, func(tp testServerParams) error {
		c, err := NewStreamConn(&StreamParams{
			URL:              tp.url,
			Reconnect:        true,
			ReconnectTimeout: 1 * time.Millisecond,
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
					"connecting->connected",
					"connected->wait_before_reconnect(websocket: close 1005 (no status))",
					"wait_before_reconnect->connecting",
					"connecting->connected",
					"connected->disconnected(websocket: close 1000 (normal))",
					"disconnected->connecting",
					"connecting->connected",
					"connected->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateAny, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
					"disconnected->connecting",
					"connecting->connected",
					"connected->wait_before_reconnect(websocket: close 1005 (no status))",
					"wait_before_reconnect->connecting",
					"connecting->connected",
					"connected->disconnected(websocket: close 1000 (normal))",
					"disconnected->connecting",
					"connecting->connected",
					"connected->disconnected(websocket: close 1000 (normal))",
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
				state: StateConnected, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"connecting->connected",
					"connecting->connected",
					"connecting->connected",
				},
			},
			testCase{
				state: StateConnected, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"connecting->connected",
					"connecting->connected",
					"connecting->connected",
				},
			},
			testCase{
				state: StateConnected, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"connecting->connected",
				},
			},
			testCase{
				state: StateConnected, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"connecting->connected",
				},
			},

			testCase{
				state: StateDisconnected, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"connected->disconnected(websocket: close 1000 (normal))",
					"connected->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateDisconnected, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
					"connected->disconnected(websocket: close 1000 (normal))",
					"connected->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: StateDisconnected, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"connected->disconnected(websocket: close 1000 (normal))",
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
					"connected->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: StateWaitBeforeReconnect, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"connected->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: StateWaitBeforeReconnect, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"connected->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: StateWaitBeforeReconnect, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"connected->wait_before_reconnect(websocket: close 1005 (no status))",
				},
			},
		}
		// }}}

		// Create state trackers for each test case
		st := make([]stateTracker, len(testCases))
		for i, v := range testCases {
			st[i] = stateTracker{}
			st[i].AddStateListener(c, v.state, StateListenerOpt{
				OneOff: v.oneOff, CallImmediately: v.callImmediately,
			})
		}

		if err := c.Connect(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(tp.rx); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		// Wait for the client identification message
		if err := waitIdMsg(tp.rx, []string{}); err != nil {
			return errors.Errorf("waiting for client identification message: %s", err)
		}

		// Reconnect
		c.closeInternal(nil, false)

		// Wait for the connection being closed
		if err := waitConnClose(tp.rx); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(tp.rx); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		// Wait for the client identification message
		if err := waitIdMsg(tp.rx, []string{}); err != nil {
			return errors.Errorf("waiting for client identification message: %s", err)
		}

		// Close and stop reconnecting
		if err := c.Close(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the connection being closed
		if err := waitConnClose(tp.rx); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		// Server gets close message before client stateListeners are called,
		// so we have to give it some time to rotate.
		time.Sleep(10 * time.Millisecond)

		// Connect again
		if err := c.Connect(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(tp.rx); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		// Wait for the client identification message
		if err := waitIdMsg(tp.rx, []string{}); err != nil {
			return errors.Errorf("waiting for client identification message: %s", err)
		}

		// Close and stop reconnecting
		if err := c.Close(); err != nil {
			return errors.Trace(err)
		}

		// Wait for the connection being closed
		if err := waitConnClose(tp.rx); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		// Server gets close message before client stateListeners are called,
		// so we have to give it some time to rotate.
		time.Sleep(10 * time.Millisecond)

		// Check states from all test cases

		for i, v := range testCases {
			if err := st[i].CheckStates(v.wantTransitions); err != nil {
				return errors.Annotatef(err, "test case #%d", i)
			}
		}

		return nil
	})
	if err != nil {
		t.Error(err)
		return
	}
}

type eventType int

const (
	eventTypeConnOpened eventType = iota
	eventTypeMsg
)

// websocketEvent represents an event like new opened connection or new
// received websocket message
type websocketEvent struct {
	eventType eventType

	// The fields below are only relevant if eventType is eventTypeMsg
	messageType int
	data        []byte
	err         error
}

func waitConnOpen(rx <-chan websocketEvent) error {
	select {
	case event := <-rx:
		if want, got := eventTypeConnOpened, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v", want, got)
		}

	case <-time.After(1 * time.Second):
		return errors.Errorf("didn't receive anything")
	}

	return nil
}

func waitIdMsg(rx <-chan websocketEvent, subs []string) error {
	select {
	case event := <-rx:
		if want, got := eventTypeMsg, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v", want, got)
		}

		var cm ProtobufClient.ClientMessage
		if err := proto.Unmarshal(event.data, &cm); err != nil {
			return errors.Trace(err)
		}

		cid := cm.GetIdentification()
		if cid == nil {
			return errors.Errorf("received something other than client identification")
		}

		// Check useragent
		{
			want := "Cryptowatch Stream Client Golang"
			got := strings.Split(cid.Useragent, "/")[0]
			if got != want {
				return errors.Errorf("Useragent: want: %q, got: %q", want, got)
			}
		}

		// Check subscriptions
		{
			want := subs
			if !(len(want) == 0 && len(subs) == 0) {
				if !reflect.DeepEqual(want, cid.Subscriptions) {
					return errors.Errorf("Subscriptions: want: %+v, got: %+v", want, cid.Subscriptions)
				}
			}
		}

	case <-time.After(1 * time.Second):
		return errors.Errorf("didn't receive anything")
	}

	return nil
}

func waitSubscribeMsg(rx <-chan websocketEvent, subs []string) error {
	select {
	case event := <-rx:
		if want, got := eventTypeMsg, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v", want, got)
		}

		var cm ProtobufClient.ClientMessage
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

func waitConnClose(rx <-chan websocketEvent) error {
	select {
	case event := <-rx:
		if want, got := eventTypeMsg, event.eventType; want != got {
			return errors.Errorf("event type: want: %v, got: %v", want, got)
		}

		if event.err == nil {
			return errors.Errorf("event.err should not be nil")
		}

	case <-time.After(1 * time.Second):
		return errors.Errorf("didn't receive anything")
	}

	return nil
}
