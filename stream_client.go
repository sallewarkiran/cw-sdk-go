package wsclient

import (
	"sync"

	pbm "code.cryptowat.ch/ws-client-go/proto/markets"
	pbs "code.cryptowat.ch/ws-client-go/proto/stream"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

const (
	defaultStreamURL = "wss://stream.cryptowat.ch"
)

// MarketDataCB defines a callback function for OnMarketData.
type MarketDataCB func(Market, MarketData)

type callMarketDataListenersReq struct {
	market    Market
	update    MarketData
	listeners []MarketDataCB
}

// PairDataCB defines a callback function for OnPairData.
type PairDataCB func(Pair, PairData)

type callPairDataListenersReq struct {
	listeners []PairDataCB
	pair      Pair
	update    PairData
}

// StreamClient is used to connect to Cryptowatch's data streaming backend.
// Typically you will get an instance using NewStreamClient(), set any state
// listeners for the connection you might need, then set data listeners for
// whatever data subscriptions you have. Finally, you can call Connect() to
// initiate the data stream.
type StreamClient struct {
	marketDataListeners     []MarketDataCB
	pairDataListeners       []PairDataCB
	callMarketDataListeners chan callMarketDataListenersReq
	callPairDataListeners   chan callPairDataListenersReq

	// We want to ensure that wsConn's methods aren't available on the
	// StreamClient to avoid confusion, so we give it explicit name.
	wsConn *wsConn

	mtx sync.Mutex
}

// NewStreamClient creates a new StreamClient instance with the given params.
// Although it starts listening for data immediately, you will still have to
// register listeners to handle that data, and then call Connect() explicitly.
func NewStreamClient(params *WSParams) (*StreamClient, error) {
	wsConn, err := newWsConn(
		params,
		&wsConnParamsInternal{
			unmarshalAuthnResult: unmarshalAuthnResultStream,
		},
	)
	if err != nil {
		return nil, errors.Trace(err)
	}

	sc := &StreamClient{
		wsConn:                  wsConn,
		callMarketDataListeners: make(chan callMarketDataListenersReq, 1),
		callPairDataListeners:   make(chan callPairDataListenersReq, 1),
	}

	sc.wsConn.onRead(func(data []byte) {
		var msg pbs.StreamMessage

		if err := proto.Unmarshal(data, &msg); err != nil {
			// Failed to parse incoming message: close connection (and if
			// reconnection was requested, then reconnect)
			sc.wsConn.disconnectOpt(nil, websocket.CloseUnsupportedData, "")
			return
		}

		switch msg.Body.(type) {
		case *pbs.StreamMessage_MarketUpdate:
			sc.marketUpdateHandler(msg.GetMarketUpdate())

		case *pbs.StreamMessage_PairUpdate:
			sc.pairUpdateHandler(msg.GetPairUpdate())

		default:
			// not a supported type
		}

	})

	go sc.listen()

	return sc, nil
}

// listen is used internally to dispatch data to registered listeners.
func (sc *StreamClient) listen() {
	for {
		select {
		case req := <-sc.callMarketDataListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		case req := <-sc.callPairDataListeners:
			for _, l := range req.listeners {
				l(req.pair, req.update)
			}
		}
	}
}

// Market data listeners

// OnMarketData sets a callback for all market data updates. MarketDataCB
// contains MarketData, which is a container for every type of update. For each
// MarketData, it will contain exactly one non-nil struct, which is one of the
// following:
// OrderBookSnapshotUpdate
// OrderBookDeltaUpdate
// OrderBookSpreadUpdate
// TradesUpdate
// IntervalsUpdate
// SummaryUpdate
// SparklineUpdate
func (sc *StreamClient) OnMarketData(cb MarketDataCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.marketDataListeners = append(sc.marketDataListeners, cb)
}

// Pair data listeners

// OnPairData sets a callback for all pair data updates. PairDataCB
// contains PairData, which is a container for every type of pair update. For
// each MarketData, there will be exactly one non-nil property, which is one of the
// following:
// VWAPUpdate
// PerformanceUpdate
// TrendlineUpdate
func (sc *StreamClient) OnPairData(cb PairDataCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.pairDataListeners = append(sc.pairDataListeners, cb)
}

// OnError registers a callback which will be called on all errors. When it's
// an error about disconnection, the OnError callbacks are called before the
// state listeners.
func (sc *StreamClient) OnError(cb OnErrorCB) {
	sc.wsConn.onError(cb)
}

// Dispatches incoming market data to registered listeners
func (sc *StreamClient) marketUpdateHandler(update *pbm.MarketUpdateMessage) {
	market := marketFromProto(update.Market)

	sc.mtx.Lock()
	marketListeners := make([]MarketDataCB, len(sc.marketDataListeners))
	copy(marketListeners, sc.marketDataListeners)
	sc.mtx.Unlock()

	switch update.Update.(type) {
	case *pbm.MarketUpdateMessage_OrderBookUpdate:
		update := orderBookSnapshotUpdateFromProto(update.GetOrderBookUpdate())
		sc.callMarketDataListeners <- callMarketDataListenersReq{
			market: market,
			update: MarketData{
				OrderBookSnapshotUpdate: &update,
			},
			listeners: marketListeners,
		}

	case *pbm.MarketUpdateMessage_OrderBookDeltaUpdate:
		update := orderBookDeltaUpdateFromProto(update.GetOrderBookDeltaUpdate())
		sc.callMarketDataListeners <- callMarketDataListenersReq{
			market: market,
			update: MarketData{
				OrderBookDeltaUpdate: &update,
			},
			listeners: marketListeners,
		}

	case *pbm.MarketUpdateMessage_OrderBookSpreadUpdate:
		update := orderBookSpreadUpdateFromProto(update.GetOrderBookSpreadUpdate())
		sc.callMarketDataListeners <- callMarketDataListenersReq{
			market: market,
			update: MarketData{
				OrderBookSpreadUpdate: &update,
			},
			listeners: marketListeners,
		}

	case *pbm.MarketUpdateMessage_TradesUpdate:
		update := tradesUpdateFromProto(update.GetTradesUpdate())
		sc.callMarketDataListeners <- callMarketDataListenersReq{
			market: market,
			update: MarketData{
				TradesUpdate: &update,
			},
			listeners: marketListeners,
		}

	case *pbm.MarketUpdateMessage_IntervalsUpdate:
		update := intervalsUpdateFromProto(update.GetIntervalsUpdate())
		sc.callMarketDataListeners <- callMarketDataListenersReq{
			market: market,
			update: MarketData{
				IntervalsUpdate: &update,
			},
			listeners: marketListeners,
		}

	case *pbm.MarketUpdateMessage_SummaryUpdate:
		update := summaryUpdateFromProto(update.GetSummaryUpdate())
		sc.callMarketDataListeners <- callMarketDataListenersReq{
			market: market,
			update: MarketData{
				SummaryUpdate: &update,
			},
			listeners: marketListeners,
		}

	case *pbm.MarketUpdateMessage_SparklineUpdate:
		update := sparklineUpdateFromProto(update.GetSparklineUpdate())
		sc.callMarketDataListeners <- callMarketDataListenersReq{
			market: market,
			update: MarketData{
				SparklineUpdate: &update,
			},
			listeners: marketListeners,
		}
	}
}

// Dispatches incoming pair data to registered listeners
func (sc *StreamClient) pairUpdateHandler(update *pbm.PairUpdateMessage) {
	pair := Pair{
		ID: uint64ToString(update.Pair),
	}

	sc.mtx.Lock()
	pairListeners := make([]PairDataCB, len(sc.pairDataListeners))
	copy(pairListeners, sc.pairDataListeners)
	sc.mtx.Unlock()

	switch update.Update.(type) {
	case *pbm.PairUpdateMessage_VwapUpdate:
		update := vwapUpdateFromProto(update.GetVwapUpdate())
		sc.callPairDataListeners <- callPairDataListenersReq{
			pair: pair,
			update: PairData{
				VWAPUpdate: &update,
			},
			listeners: pairListeners,
		}

	case *pbm.PairUpdateMessage_PerformanceUpdate:
		update := performanceUpdateFromProto(update.GetPerformanceUpdate())
		sc.callPairDataListeners <- callPairDataListenersReq{
			pair: pair,
			update: PairData{
				PerformanceUpdate: &update,
			},
			listeners: pairListeners,
		}

	case *pbm.PairUpdateMessage_TrendlineUpdate:
		update := trendlineUpdateFromProto(update.GetTrendlineUpdate())
		sc.callPairDataListeners <- callPairDataListenersReq{
			pair: pair,
			update: PairData{
				TrendlineUpdate: &update,
			},
			listeners: pairListeners,
		}
	}
}

// OnStateChange registers a new listener for the given state. The listener is
// registered with the default options (call the listener every time the state
// becomes active, and don't call the listener immediately for the current
// state). All registered callbacks for all states (and all messages, see
// OnMarketData) will be called by the same internal goroutine, i.e. they are
// never called concurrently with each other.
//
// The order of listeners invocation for the same state is unspecified, and
// clients shouldn't rely on it.
//
// The listeners shouldn't block; a blocked listener will also block the whole
// stream connection.
//
// To subscribe to all state changes, use ConnStateAny as a state.
func (sc *StreamClient) OnStateChange(state ConnState, cb StateCallback) {
	sc.wsConn.onStateChange(state, cb)
}

// OnStateChangeOpt is like OnStateChange, but also takes additional
// options; see StateListenerOpt for details.
func (sc *StreamClient) OnStateChangeOpt(state ConnState, cb StateCallback, opt StateListenerOpt) {
	sc.wsConn.onStateChangeOpt(state, cb, opt)
}

// GetSubscriptions returns a slice of the current subscriptions.
func (sc *StreamClient) GetSubscriptions() []string {
	return sc.wsConn.getSubscriptions()
}

// OnConnClosed allows the client to set a callback for when the connection is lost.
// The new state of the client could be ConnStateDisconnected or ConnStateWaitBeforeReconnect.
func (sc *StreamClient) OnConnClosed(cb ConnClosedCallback) {
	sc.wsConn.onConnClosed(cb)
}

// Subscribe makes a request to subscribe to the given keys. Example:
//
//   client.Subscribe([]string{
//           "markets:1:book:deltas",
//           "markets:1:book:spread",
//   })
//
// The client must be connected and authenticated for this to work. See
// WSParams.Subscriptions for more details.
func (sc *StreamClient) Subscribe(keys []string) error {
	return sc.wsConn.subscribe(keys)
}

// Unsubscribe unsubscribes from the given set of keys. Also see notes for
// subscribe.
func (sc *StreamClient) Unsubscribe(keys []string) error {
	return sc.wsConn.unsubscribe(keys)
}

// URL returns the url the client is connected to, e.g. wss://stream.cryptowat.ch.
func (sc *StreamClient) URL() string {
	return sc.wsConn.url()
}

// Connect either starts a connection goroutine (if state is
// ConnStateDisconnected), or makes it connect immediately, ignoring timeout
// (if the state is ConnStateWaitBeforeReconnect). For other states, this returns an
// error.
//
// Connect doesn't wait for the connection to establish; it returns immediately.
func (sc *StreamClient) Connect() (err error) {
	return sc.wsConn.connect()
}

// Close stops the connection (or reconnection loop, if active), and if
// websocket connection is active at the moment, closes it as well.
func (sc *StreamClient) Close() (err error) {
	return sc.wsConn.close()
}
