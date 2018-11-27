package wsclient

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

	pbb "code.cryptowat.ch/ws-client-go/proto/broker"
	pbc "code.cryptowat.ch/ws-client-go/proto/client"
	pbm "code.cryptowat.ch/ws-client-go/proto/markets"
	pbs "code.cryptowat.ch/ws-client-go/proto/stream"
	"code.cryptowat.ch/ws-client-go/internal"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

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

type testServerParams struct {
	rx  <-chan websocketEvent
	tx  chan<- internal.WebsocketTx
	url string
}

type serverType int32

type onConnClosedCB struct {
	state ConnState
	cause error
}

const (
	streamServer serverType = iota
	brokerServer
)

func withTestServer(
	server serverType,
	t *testing.T,
	cb func(tp *testServerParams) error,
) error {
	// tx and rx are channels to communicate raw websocket messages with the
	// test server: everything received by the server will be delivered to rx,
	// and everything sent to tx will be sent by the server to the client.
	rx := make(chan websocketEvent, 128)
	tx := make(chan internal.WebsocketTx, 128)

	// Create test server with a single root endpoint which upgrades connection
	// to websocket
	ts := httptest.NewServer(http.HandlerFunc(getStreamHandler(server, t, rx, tx)))
	defer ts.Close()

	// Replace the scheme in url to "ws"
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

// getRootHandler returns an http handler which upgrades the connection to
// websocket, forwards events (opened connections and received messages) to the
// rx channel, and forwards messages from tx channel to websocket.
//
// NOTE that only one connection should be opened at a time, since currently
// there's no way to receive/send stuff from/to a particular connection in case
// there are many.
func getStreamHandler(
	server serverType,
	t *testing.T,
	rx chan<- websocketEvent,
	tx <-chan internal.WebsocketTx,
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

		t.Logf("new stream websocket conn is opened")

		rx <- websocketEvent{
			eventType: eventTypeConnOpened,
		}

		go func() {
			for {
				mt, message, err := ws.ReadMessage()
				var unmarshalledStr string

				switch server {
				case streamServer:
					var unmarshalled pbs.StreamMessage
					if err := proto.Unmarshal(message, &unmarshalled); err != nil {
						unmarshalledStr = fmt.Sprintf("failed to unmarshal: %s", err)
					} else {
						unmarshalledStr = proto.CompactTextString(&unmarshalled)
					}
				case brokerServer:
					var unmarshalled pbb.BrokerUpdateMessage
					if err := proto.Unmarshal(message, &unmarshalled); err != nil {
						unmarshalledStr = fmt.Sprintf("failed to unmarshal: %s", err)
					} else {
						unmarshalledStr = proto.CompactTextString(&unmarshalled)
					}
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
				var unmarshalledStr string

				switch server {
				case streamServer:
					var unmarshalled pbs.StreamMessage
					if err := proto.Unmarshal(msg.Data, &unmarshalled); err != nil {
						unmarshalledStr = fmt.Sprintf("failed to unmarshal: %s", err)
					} else {
						unmarshalledStr = proto.CompactTextString(&unmarshalled)
					}

				case brokerServer:
					var unmarshalled pbb.BrokerUpdateMessage
					if err := proto.Unmarshal(msg.Data, &unmarshalled); err != nil {
						unmarshalledStr = fmt.Sprintf("failed to unmarshal: %s", err)
					} else {
						unmarshalledStr = proto.CompactTextString(&unmarshalled)
					}
				}

				t.Logf("websocket tx: type=%d, data=%+v (%s)", msg.MessageType, msg.Data, unmarshalledStr)

				if err := ws.WriteMessage(msg.MessageType, msg.Data); err != nil {
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

func TestWsConn(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		// marketRx is a channel to which all MarketUpdateMessage's received by
		// the client will be delivered.
		// TODO test the generic handler
		// marketRx := make(chan *pbm.MarketUpdateMessage, 128)

		client, err := NewStreamClient(&WSParams{
			URL:           tp.url,
			Subscriptions: testSubscriptions,

			APIKey:    testApiKey1,
			SecretKey: testSecretKey1,
		})
		if err != nil {
			return errors.Trace(err)
		}

		// Add state tracker to the connection, so we'll see all state transitions
		st := NewStateTracker()
		st.addStateListener(client.wsConn, ConnStateAny, StateListenerOpt{})

		// TODO use the generic handler here when ready
		// client.AddMarketListener(
		// 	func(msg *pbm.MarketUpdateMessage) {
		// 		marketRx <- msg
		// 	},
		// )

		if err := client.Connect(); err != nil {
			return errors.Trace(err)
		}

		if err := st.expectState(ConnStateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st.expectState(ConnStateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendStreamAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st.expectState(ConnStateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Subscribe to one more topic
		if err := client.Subscribe([]string{"baz"}); err != nil {
			return errors.Errorf("subscribing to baz: %s", err)
		}

		// Wait for the subscribe-to-baz message
		if err := waitSubscribeMsg(t, tp, []string{"baz"}); err != nil {
			return errors.Errorf("waiting for subscribe message: %s", err)
		}

		// Check states so far
		if err := st.checkStates([]string{
			"disconnected->connecting",
			"connecting->authenticating",
			"authenticating->established",
		}); err != nil {
			return errors.Trace(err)
		}

		// Make sure marketRx is empty
		// select {
		// case <-marketRx:
		// 	return errors.Errorf("marketRx should be empty")
		// default:
		// 	// All right, emtpy.
		// }

		// Send heartbeat (should be ignored by the client)
		tp.tx <- internal.WebsocketTx{
			MessageType: websocket.BinaryMessage,
			Data:        []byte{1},
		}

		// Send MarketUpdateMessage to the client {{{
		mm := &pbs.StreamMessage{
			Body: &pbs.StreamMessage_MarketUpdate{
				MarketUpdate: &pbm.MarketUpdateMessage{
					Market: &pbm.Market{
						ExchangeId:     1,
						CurrencyPairId: 1,
						MarketId:       1,
					},
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

		tp.tx <- internal.WebsocketTx{
			MessageType: websocket.BinaryMessage,
			Data:        data,
		}
		// }}}

		// TODO use the generic handler
		// Wait for MarketUpdateMessage {{{
		// if err := func() error {
		// 	select {
		// 	case mm := <-marketRx:
		// 		tu := mm.GetTradesUpdate()
		// 		if tu == nil {
		// 			return errors.Errorf("received something other than TradesUpdate")
		// 		}

		// 		// Check message contents
		// 		if want, got := int64(1), tu.Trades[0].Id; want != got {
		// 			return errors.Errorf("Id: want: %v, got: %v", want, got)
		// 		}

		// 		if want, got := float32(2), tu.Trades[0].Price; want != got {
		// 			return errors.Errorf("Price: want: %v, got: %v", want, got)
		// 		}

		// 		if want, got := float32(3), tu.Trades[0].Amount; want != got {
		// 			return errors.Errorf("Amount: want: %v, got: %v", want, got)
		// 		}

		// 	case <-time.After(1 * time.Second):
		// 		return errors.Errorf("didn't receive anything")
		// 	}

		// 	return nil
		// }(); err != nil {
		// 	return errors.Errorf("waiting for MarketUpdateMessage: %s", err)
		// }
		// }}}

		// Send garbage, which should result in a reconnection
		tp.tx <- internal.WebsocketTx{
			MessageType: websocket.BinaryMessage,
			Data:        []byte{1, 2, 3},
		}

		// Wait for the connection being closed
		if err := waitConnClose(t, tp); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		if err := st.expectState(ConnStateWaitBeforeReconnect); err != nil {
			return errors.Trace(err)
		}

		if err := st.expectState(ConnStateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st.expectState(ConnStateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendStreamAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st.expectState(ConnStateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Check states so far
		if err := st.checkStates([]string{
			"disconnected->connecting",
			"connecting->authenticating",
			"authenticating->established",
			"established->wait-before-reconnect(websocket: close 1003 (unsupported data))",
			"wait-before-reconnect->connecting",
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

func TestStateListeners(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		c, err := NewStreamClient(&WSParams{
			URL: tp.url,

			APIKey:        testApiKey1,
			SecretKey:     testSecretKey1,
			Subscriptions: testSubscriptions,
		})
		if err != nil {
			return errors.Trace(err)
		}

		type testCase struct {
			state                   ConnState
			oneOff, callImmediately bool
			wantTransitions         []string
		}

		// Init test cases table {{{
		testCases := []testCase{
			testCase{
				state: ConnStateAny, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"disconnected->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->wait-before-reconnect(websocket: close 1005 (no status))",
					"wait-before-reconnect->connecting",
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
				state: ConnStateAny, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
					"disconnected->connecting",
					"connecting->authenticating",
					"authenticating->established",
					"established->wait-before-reconnect(websocket: close 1005 (no status))",
					"wait-before-reconnect->connecting",
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
				state: ConnStateAny, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"disconnected->connecting",
				},
			},
			testCase{
				state: ConnStateAny, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
				},
			},

			testCase{
				state: ConnStateEstablished, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"authenticating->established",
					"authenticating->established",
					"authenticating->established",
				},
			},
			testCase{
				state: ConnStateEstablished, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"authenticating->established",
					"authenticating->established",
					"authenticating->established",
				},
			},
			testCase{
				state: ConnStateEstablished, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"authenticating->established",
				},
			},
			testCase{
				state: ConnStateEstablished, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"authenticating->established",
				},
			},

			testCase{
				state: ConnStateDisconnected, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"established->disconnected(websocket: close 1000 (normal))",
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: ConnStateDisconnected, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
					"established->disconnected(websocket: close 1000 (normal))",
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: ConnStateDisconnected, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"established->disconnected(websocket: close 1000 (normal))",
				},
			},
			testCase{
				state: ConnStateDisconnected, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"disconnected->disconnected",
				},
			},

			testCase{
				state: ConnStateWaitBeforeReconnect, oneOff: false, callImmediately: false,
				wantTransitions: []string{
					"established->wait-before-reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: ConnStateWaitBeforeReconnect, oneOff: false, callImmediately: true,
				wantTransitions: []string{
					"established->wait-before-reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: ConnStateWaitBeforeReconnect, oneOff: true, callImmediately: false,
				wantTransitions: []string{
					"established->wait-before-reconnect(websocket: close 1005 (no status))",
				},
			},
			testCase{
				state: ConnStateWaitBeforeReconnect, oneOff: true, callImmediately: true,
				wantTransitions: []string{
					"established->wait-before-reconnect(websocket: close 1005 (no status))",
				},
			},
		}
		// }}}

		// Create state trackers for each test case
		st := make([]*stateTracker, len(testCases))
		for i, v := range testCases {
			st[i] = NewStateTracker()
			st[i].addStateListener(c.wsConn, v.state, StateListenerOpt{
				OneOff: v.oneOff, CallImmediately: v.callImmediately,
			})
		}

		// TODO this fails for some reason
		// onConnClosedCalled := make(chan onConnClosedCB, 1)
		// c.OnConnClosed(func(state ConnState, cause error) {
		// 	onConnClosedCalled <- onConnClosedCB{
		// 		state: state,
		// 		cause: cause,
		// 	}
		// })

		if err := c.Connect(); err != nil {
			return errors.Trace(err)
		}

		if err := st[0].expectState(ConnStateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st[0].expectState(ConnStateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendStreamAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st[0].expectState(ConnStateEstablished); err != nil {
			return errors.Trace(err)
		}

		// Reconnect
		c.wsConn.transport.CloseOpt(nil, false)

		// Wait for the connection being closed
		if err := waitConnClose(t, tp); err != nil {
			return errors.Errorf("waiting for connection being closed: %s", err)
		}

		// if err := waitOnConnClosedCallback(onConnClosedCalled, StateWaitBeforeReconnect); err != nil {
		// 	return errors.Trace(err)
		// }

		if err := st[0].expectState(ConnStateWaitBeforeReconnect); err != nil {
			return errors.Trace(err)
		}

		if err := st[0].expectState(ConnStateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st[0].expectState(ConnStateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendStreamAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st[0].expectState(ConnStateEstablished); err != nil {
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

		if err := st[0].expectState(ConnStateDisconnected); err != nil {
			return errors.Trace(err)
		}

		// Connect again
		if err := c.Connect(); err != nil {
			return errors.Trace(err)
		}

		if err := st[0].expectState(ConnStateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st[0].expectState(ConnStateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err != nil {
			return errors.Errorf("waiting for authn request: %s", err)
		}

		// Send AuthenticationResult to the client
		if err := sendStreamAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
			return errors.Errorf("sending authn resp: %s", err)
		}

		if err := st[0].expectState(ConnStateEstablished); err != nil {
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

		if err := st[0].expectState(ConnStateDisconnected); err != nil {
			return errors.Trace(err)
		}

		// Check states from all test cases

		for i, v := range testCases {
			if err := st[i].checkStates(v.wantTransitions); err != nil {
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

func TestAuthnErrors(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		client, err := NewStreamClient(&WSParams{
			URL:           tp.url,
			Subscriptions: testSubscriptions,

			APIKey:    testApiKey1,
			SecretKey: testSecretKeyWrong,
		})
		if err != nil {
			return errors.Trace(err)
		}

		// Add state tracker to the connection, so we'll see all state transitions
		st := NewStateTracker()
		st.addStateListener(client.wsConn, ConnStateAny, StateListenerOpt{})

		if err := client.Connect(); err != nil {
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
			if err := st.expectState(ConnStateConnecting); err != nil {
				return errors.Trace(err)
			}

			// Wait for the new conn to be opened
			if err := waitConnOpen(t, tp); err != nil {
				return errors.Errorf("waiting for new conn to be opened: %s", err)
			}

			if err := st.expectState(ConnStateAuthenticating); err != nil {
				return errors.Trace(err)
			}

			// Try to authenticate a certain number of times
			for i := 0; i < tc.retry; i++ {
				// Wait for the authentication request
				if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err == nil {
					return errors.Errorf("authn request should be wrong")
				}

				// Send AuthenticationResult to the client
				if err := sendStreamAuthnResp(t, tp, tc.returnedStatus); err != nil {
					return errors.Errorf("sending authn resp: %s", err)
				}
			}

			// Wait for the conn to be closed
			if err := waitConnClose(t, tp); err != nil {
				return errors.Errorf("waiting for connection being closed: %s", err)
			}

			// TODO this fails intermittently both locally and on CI. need to figure out why
			if err := st.expectStateWCause(ConnStateWaitBeforeReconnect, tc.expectedCause); err != nil {
				return errors.Trace(err)
			}
		}

		// Now, finally connect successfully {{{
		if err := st.expectState(ConnStateConnecting); err != nil {
			return errors.Trace(err)
		}

		// Wait for the new conn to be opened
		if err := waitConnOpen(t, tp); err != nil {
			return errors.Errorf("waiting for new conn to be opened: %s", err)
		}

		if err := st.expectState(ConnStateAuthenticating); err != nil {
			return errors.Trace(err)
		}

		// Wait for the authentication request
		if err := waitAuthnReq(t, tp, testApiKey1, testSecretKey1); err == nil {
			return errors.Errorf("authn request should be wrong")
		}

		// Send AuthenticationResult to the client
		if err := sendStreamAuthnResp(t, tp, pbs.AuthenticationResult_AUTHENTICATED); err != nil {
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

func waitOnConnClosedCallback(onConnClosedCalled chan onConnClosedCB, expectedState ConnState) error {
	select {
	case connClosed := <-onConnClosedCalled:
		if connClosed.state != expectedState {
			return errors.Errorf("Conn closed expected state %v, got %v", expectedState, connClosed.state)
		}

	case <-time.After(1 * time.Second):
		return errors.New("OnConnClosed was never called")
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
		return errors.Errorf("didn't receive auth response")
	}

	return nil
}

func sendStreamAuthnResp(t *testing.T, tp *testServerParams, status pbs.AuthenticationResult_Status) error {
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

	tp.tx <- internal.WebsocketTx{
		MessageType: websocket.BinaryMessage,
		Data:        data,
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

// stateTracker {{{
type stateChange struct {
	oldState, state ConnState
	cause           error
}

type stateTracker struct {
	states  []string
	mtx     sync.Mutex
	changes chan stateChange
}

func NewStateTracker() *stateTracker {
	return &stateTracker{
		changes: make(chan stateChange, 1024),
	}
}

func (st *stateTracker) addStateListener(conn *wsConn, state ConnState, opt StateListenerOpt) {
	conn.AddStateListenerOpt(
		state,
		func(oldState, state ConnState, cause error) {
			st.mtx.Lock()
			defer st.mtx.Unlock()

			errStr := ""
			if cause != nil {
				errStr = fmt.Sprintf("(%s)", cause)
			}

			st.states = append(st.states, fmt.Sprintf("%s->%s%s", ConnStateNames[oldState], ConnStateNames[state], errStr))

			st.changes <- stateChange{
				oldState: oldState,
				state:    state,
				cause:    cause,
			}
		},
		opt,
	)
}

func (st *stateTracker) checkStates(want []string) error {
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

func (st *stateTracker) expectState(state ConnState) error {
	return st.expectStateWCause(state, dontCheckErr)
}

func (st *stateTracker) expectStateWCause(state ConnState, cause error) error {
	select {
	case change := <-st.changes:
		if change.state != state {
			return errors.Errorf("expect state change: want: %s, got: %s (%v)", ConnStateNames[state], ConnStateNames[change.state], change)
		}

		if cause != dontCheckErr && errors.Cause(change.cause) != cause {
			return errors.Errorf("expect state cause: want: %s, got: %s (%v)", cause, change.cause, change)
		}

	case <-time.After(2 * time.Second):
		return errors.Errorf("expect state change: want: %s, but nothing happened", ConnStateNames[state])
	}

	return nil
}

// statetracker }}}

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

const (
	testApiKey1        = "foo"
	testSecretKey1     = "YmFy"         // base64-encoded "bar"
	testSecretKeyWrong = "YmFyYmFyYmFy" // base64-encoded "barbarbar"
)

var testSubscriptions = []string{"foo", "bar"}

// testWriteToNonConnected ensures that sending to a non-established StreamConn
// results in an error
func testWriteToNonConnected(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		conn, err := newWsConn(&WSParams{
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

// testConnectConnected ensures that calling Connect on a connection with
// active connection loop results in an error
func testConnectConnected(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		c, err := newWsConn(&WSParams{
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

// testConnectConnected ensures that calling Connect on a connection with
// active connection loop results in an error
func testCloseClosed(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		c, err := newWsConn(&WSParams{
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

func testSubscribe(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		conn, err := newWsConn(&WSParams{
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

func testDefaultURL(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		conn, err := newWsConn(&WSParams{})
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

func testDefaultOptions(t *testing.T) {
	err := withTestServer(streamServer, t, func(tp *testServerParams) error {
		conn, err := newWsConn(&WSParams{})
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
