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

// Signatures for market and pair data callbacks {{

// Market data callbacks

// OrderBookSnapshotUpdateCB defines a callback function for OnOrderBookSnapshotUpdate.
type OrderBookSnapshotUpdateCB func(Market, OrderBookSnapshotUpdate)

// OrderBookDeltaUpdateCB defines a callback function for OnOrderBookDeltaUpdate.
type OrderBookDeltaUpdateCB func(Market, OrderBookDeltaUpdate)

// OrderBookSpreadUpdateCB defines a callback function for OnOrderBookSpreadUpdate.
type OrderBookSpreadUpdateCB func(Market, OrderBookSpreadUpdate)

// PublicTradesUpdateCB defines a callback function for OnTradesUpdate.
type PublicTradesUpdateCB func(Market, TradesUpdate)

// IntervalsUpdateCB defines a callback function for OnIntervalsUpdate.
type IntervalsUpdateCB func(Market, IntervalsUpdate)

// SummaryUpdateCB defines a callback function for OnSummaryUpdate.
type SummaryUpdateCB func(Market, SummaryUpdate)

// SparklineUpdateCB defines a callback function for OnSparklineUpdate.
type SparklineUpdateCB func(Market, SparklineUpdate)

type callOrderBookUpdateListenersReq struct {
	market    Market
	update    OrderBookSnapshotUpdate
	listeners []OrderBookSnapshotUpdateCB
}

type callOrderBookDeltaUpdateListenersReq struct {
	market    Market
	update    OrderBookDeltaUpdate
	listeners []OrderBookDeltaUpdateCB
}

type callOrderBookSpreadUpdateListenersReq struct {
	market    Market
	update    OrderBookSpreadUpdate
	listeners []OrderBookSpreadUpdateCB
}

type callTradesUpdateListenersReq struct {
	market    Market
	update    TradesUpdate
	listeners []PublicTradesUpdateCB
}

type callIntervalsUpdateListenersReq struct {
	market    Market
	update    IntervalsUpdate
	listeners []IntervalsUpdateCB
}

type callSummaryUpdateListenersReq struct {
	market    Market
	update    SummaryUpdate
	listeners []SummaryUpdateCB
}

type callSparklineUpdateListenersReq struct {
	market    Market
	update    SparklineUpdate
	listeners []SparklineUpdateCB
}

// Pair data callbacks

// VWAPUpdateCB defines a callback function for currency pair VWAP updates.
type VWAPUpdateCB func(Pair, VWAPUpdate)

// PerformanceUpdateCB defines a callback function for currency pair performance updates.
type PerformanceUpdateCB func(Pair, PerformanceUpdate)

// TrendlineUpdateCB defines a callback function for currency pair trendline updates.
type TrendlineUpdateCB func(Pair, TrendlineUpdate)

type callvwapUpdateListenersReq struct {
	listeners []VWAPUpdateCB
	pair      Pair
	update    VWAPUpdate
}

type callPairPerformanceUpdateListenersReq struct {
	listeners []PerformanceUpdateCB
	pair      Pair
	update    PerformanceUpdate
}

type callPairTrendlineUpdateListenersReq struct {
	listeners []TrendlineUpdateCB
	pair      Pair
	update    TrendlineUpdate
}

// }}

// StreamClient is used to connect to Cryptowatch's data streaming backend.
// Typically you will get an instance using NewStreamClient(), set any state
// listeners for the connection you might need, then set data listeners for
// whatever data subscriptions you have. Finally, you can call Connect() to
// initiate the data stream.
type StreamClient struct {
	// Market updates
	callOrderBookUpdateListeners       chan callOrderBookUpdateListenersReq
	callOrderBookDeltaUpdateListeners  chan callOrderBookDeltaUpdateListenersReq
	callOrderBookSpreadUpdateListeners chan callOrderBookSpreadUpdateListenersReq
	callTradesUpdateListeners          chan callTradesUpdateListenersReq
	callIntervalsUpdateListeners       chan callIntervalsUpdateListenersReq
	callSummaryUpdateListeners         chan callSummaryUpdateListenersReq
	callSparklineUpdateListeners       chan callSparklineUpdateListenersReq
	orderBookUpdateListeners           []OrderBookSnapshotUpdateCB
	orderBookDeltaUpdateListeners      []OrderBookDeltaUpdateCB
	orderBookSpreadUpdateListeners     []OrderBookSpreadUpdateCB
	tradesUpdateListeners              []PublicTradesUpdateCB
	intervalsUpdateListeners           []IntervalsUpdateCB
	summaryUpdateListeners             []SummaryUpdateCB
	sparklineUpdateListeners           []SparklineUpdateCB

	// Pair updates
	callVWAPUpdateListeners            chan callvwapUpdateListenersReq
	callPairPerformanceUpdateListeners chan callPairPerformanceUpdateListenersReq
	callPairTrendlineUpdateListeners   chan callPairTrendlineUpdateListenersReq
	vwapUpdateListeners                []VWAPUpdateCB
	pairPerformanceUpdateListeners     []PerformanceUpdateCB
	pairTrendlineUpdateListeners       []TrendlineUpdateCB

	*wsConn
	mtx sync.Mutex
}

// NewStreamClient creates a new StreamClient instance with the given params.
// Although it starts listening for data immediately, you will still have to
// register listeners to handle that data, and then call Connect() explicitly.
func NewStreamClient(params *WSParams) (*StreamClient, error) {
	wsConn, err := newWsConn(
		params,
		&wsConnParamsInternal{
			unmarshalAuthnResult: func(data []byte) (*pbs.AuthenticationResult, error) {
				var msg pbs.StreamMessage

				if err := proto.Unmarshal(data, &msg); err != nil {
					return nil, errors.Trace(err)
				}

				authnResult := msg.GetAuthenticationResult()
				if authnResult == nil {
					return nil, errors.Errorf("not an authentication result")
				}

				return authnResult, nil
			},
		},
	)
	if err != nil {
		return nil, errors.Trace(err)
	}

	sc := &StreamClient{
		wsConn: wsConn,

		callOrderBookUpdateListeners:       make(chan callOrderBookUpdateListenersReq, 1),
		callOrderBookDeltaUpdateListeners:  make(chan callOrderBookDeltaUpdateListenersReq, 1),
		callOrderBookSpreadUpdateListeners: make(chan callOrderBookSpreadUpdateListenersReq, 1),
		callTradesUpdateListeners:          make(chan callTradesUpdateListenersReq, 1),
		callIntervalsUpdateListeners:       make(chan callIntervalsUpdateListenersReq, 1),
		callSummaryUpdateListeners:         make(chan callSummaryUpdateListenersReq, 1),
		callSparklineUpdateListeners:       make(chan callSparklineUpdateListenersReq, 1),

		callVWAPUpdateListeners:            make(chan callvwapUpdateListenersReq, 1),
		callPairPerformanceUpdateListeners: make(chan callPairPerformanceUpdateListenersReq, 1),
		callPairTrendlineUpdateListeners:   make(chan callPairTrendlineUpdateListenersReq, 1),
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
		// Market listeners
		case req := <-sc.callOrderBookUpdateListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		case req := <-sc.callOrderBookDeltaUpdateListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		case req := <-sc.callOrderBookSpreadUpdateListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		case req := <-sc.callTradesUpdateListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		case req := <-sc.callIntervalsUpdateListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		case req := <-sc.callSummaryUpdateListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		case req := <-sc.callSparklineUpdateListeners:
			for _, l := range req.listeners {
				l(req.market, req.update)
			}

		// Pair listeners
		case req := <-sc.callVWAPUpdateListeners:
			for _, l := range req.listeners {
				l(req.pair, req.update)
			}

		case req := <-sc.callPairPerformanceUpdateListeners:
			for _, l := range req.listeners {
				l(req.pair, req.update)
			}

		case req := <-sc.callPairTrendlineUpdateListeners:
			for _, l := range req.listeners {
				l(req.pair, req.update)
			}
		}
	}
}

// Market data listeners

// OnOrderBookSnapshotUpdate sets a callback for market order book updates.
// Each time the order book updates on the exchange, a new snapshot is received. The updates
// are throttled at 1 snapshot minute.
// See also OnOrderBookDeltaUpdate
func (sc *StreamClient) OnOrderBookSnapshotUpdate(cb OrderBookSnapshotUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.orderBookUpdateListeners = append(sc.orderBookUpdateListeners, cb)
}

// OnOrderBookDeltaUpdate sets a callback for market order book delta updates.
// Each time the order book changes on the exchange, a delta is received instead of the full snapshot.
// Delta updates are not throttled like the snapshot updates are.
func (sc *StreamClient) OnOrderBookDeltaUpdate(cb OrderBookDeltaUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.orderBookDeltaUpdateListeners = append(sc.orderBookDeltaUpdateListeners, cb)
}

// OnOrderBookSpreadUpdate sets a callback for market order book spread updates.
// Each time the best asking or bidding price changes, a spread update is sent with the new values.
func (sc *StreamClient) OnOrderBookSpreadUpdate(cb OrderBookSpreadUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.orderBookSpreadUpdateListeners = append(sc.orderBookSpreadUpdateListeners, cb)
}

// OnTradesUpdate sets a callback for a market's trade updates. Whenever trades are executed,
// this callback will be called with the new trades.
func (sc *StreamClient) OnTradesUpdate(cb PublicTradesUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.tradesUpdateListeners = append(sc.tradesUpdateListeners, cb)
}

// OnIntervalsUpdate sets a callback for a market's interval updates.
// Intervals are used to draw charts at different Period levels. Use the Period
// definitions to determine which interval is updated.
func (sc *StreamClient) OnIntervalsUpdate(cb IntervalsUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.intervalsUpdateListeners = append(sc.intervalsUpdateListeners, cb)
}

// OnSummaryUpdate sets a callback for a market's summary updates.
// A summary update includes recent information on price, volume, and trades,
// and is throttled to one update every 5 seconds.
func (sc *StreamClient) OnSummaryUpdate(cb SummaryUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.summaryUpdateListeners = append(sc.summaryUpdateListeners, cb)
}

// OnSparklineUpdate sets a callback for a market's sparkline updates.
func (sc *StreamClient) OnSparklineUpdate(cb SparklineUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.sparklineUpdateListeners = append(sc.sparklineUpdateListeners, cb)
}

// Pair data listeners

// OnVWAPUpdate sets a callback for a currency pair's VWAP (volume weighted average price) updates.
func (sc *StreamClient) OnVWAPUpdate(cb VWAPUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.vwapUpdateListeners = append(sc.vwapUpdateListeners, cb)
}

// OnPairPerformanceUpdate sets a callback for a currency pair's performance updates.
// TODO describe what performanc means in this context.
func (sc *StreamClient) OnPairPerformanceUpdate(cb PerformanceUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.pairPerformanceUpdateListeners = append(sc.pairPerformanceUpdateListeners, cb)
}

// OnPairTrendlineUpdate sets a callback for a currency pair's trendline updates.
// TODO describe what trendline means
func (sc *StreamClient) OnPairTrendlineUpdate(cb TrendlineUpdateCB) {
	sc.mtx.Lock()
	defer sc.mtx.Unlock()

	sc.pairTrendlineUpdateListeners = append(sc.pairTrendlineUpdateListeners, cb)
}

// Dispatches incoming market data to registered listeners
func (sc *StreamClient) marketUpdateHandler(update *pbm.MarketUpdateMessage) {
	market := marketFromProto(update.Market)

	switch update.Update.(type) {
	case *pbm.MarketUpdateMessage_OrderBookUpdate:
		sc.mtx.Lock()
		listeners := make([]OrderBookSnapshotUpdateCB, len(sc.orderBookUpdateListeners))
		copy(listeners, sc.orderBookUpdateListeners)
		sc.mtx.Unlock()
		sc.callOrderBookUpdateListeners <- callOrderBookUpdateListenersReq{
			market:    market,
			update:    orderBookUpdateFromProto(update.GetOrderBookUpdate()),
			listeners: listeners,
		}

	case *pbm.MarketUpdateMessage_OrderBookDeltaUpdate:
		sc.mtx.Lock()
		listeners := make([]OrderBookDeltaUpdateCB, len(sc.orderBookDeltaUpdateListeners))
		copy(listeners, sc.orderBookDeltaUpdateListeners)
		sc.mtx.Unlock()
		sc.callOrderBookDeltaUpdateListeners <- callOrderBookDeltaUpdateListenersReq{
			market:    market,
			update:    orderBookDeltaUpdateFromProto(update.GetOrderBookDeltaUpdate()),
			listeners: listeners,
		}

	case *pbm.MarketUpdateMessage_OrderBookSpreadUpdate:
		sc.mtx.Lock()
		listeners := make([]OrderBookSpreadUpdateCB, len(sc.orderBookSpreadUpdateListeners))
		copy(listeners, sc.orderBookSpreadUpdateListeners)
		sc.mtx.Unlock()
		sc.callOrderBookSpreadUpdateListeners <- callOrderBookSpreadUpdateListenersReq{
			market:    market,
			update:    orderBookSpreadUpdateFromProto(update.GetOrderBookSpreadUpdate()),
			listeners: listeners,
		}

	case *pbm.MarketUpdateMessage_TradesUpdate:
		sc.mtx.Lock()
		listeners := make([]PublicTradesUpdateCB, len(sc.tradesUpdateListeners))
		copy(listeners, sc.tradesUpdateListeners)
		sc.mtx.Unlock()
		sc.callTradesUpdateListeners <- callTradesUpdateListenersReq{
			market:    market,
			update:    tradesUpdateFromProto(update.GetTradesUpdate()),
			listeners: listeners,
		}

	case *pbm.MarketUpdateMessage_IntervalsUpdate:
		sc.mtx.Lock()
		listeners := make([]IntervalsUpdateCB, len(sc.intervalsUpdateListeners))
		copy(listeners, sc.intervalsUpdateListeners)
		sc.mtx.Unlock()
		sc.callIntervalsUpdateListeners <- callIntervalsUpdateListenersReq{
			market:    market,
			update:    intervalsUpdateFromProto(update.GetIntervalsUpdate()),
			listeners: listeners,
		}

	case *pbm.MarketUpdateMessage_SummaryUpdate:
		sc.mtx.Lock()
		listeners := make([]SummaryUpdateCB, len(sc.summaryUpdateListeners))
		copy(listeners, sc.summaryUpdateListeners)
		sc.mtx.Unlock()
		sc.callSummaryUpdateListeners <- callSummaryUpdateListenersReq{
			market:    market,
			update:    summaryUpdateFromProto(update.GetSummaryUpdate()),
			listeners: listeners,
		}

	case *pbm.MarketUpdateMessage_SparklineUpdate:
		sc.mtx.Lock()
		listeners := make([]SparklineUpdateCB, len(sc.sparklineUpdateListeners))
		copy(listeners, sc.sparklineUpdateListeners)
		sc.mtx.Unlock()
		sc.callSparklineUpdateListeners <- callSparklineUpdateListenersReq{
			market:    market,
			update:    sparklineUpdateFromProto(update.GetSparklineUpdate()),
			listeners: listeners,
		}
	}
}

// Dispatches incoming pair data to registered listeners
func (sc *StreamClient) pairUpdateHandler(update *pbm.PairUpdateMessage) {
	pair := Pair{
		ID: uint64ToString(update.Pair),
	}

	switch update.Update.(type) {
	case *pbm.PairUpdateMessage_VwapUpdate:
		sc.mtx.Lock()
		listeners := make([]VWAPUpdateCB, len(sc.vwapUpdateListeners))
		copy(listeners, sc.vwapUpdateListeners)
		sc.mtx.Unlock()
		vwap := vwapUpdateFromProto(update.GetVwapUpdate())
		sc.callVWAPUpdateListeners <- callvwapUpdateListenersReq{
			pair:      pair,
			update:    vwap,
			listeners: listeners,
		}

	case *pbm.PairUpdateMessage_PerformanceUpdate:
		sc.mtx.Lock()
		listeners := make([]PerformanceUpdateCB, len(sc.pairPerformanceUpdateListeners))
		copy(listeners, sc.pairPerformanceUpdateListeners)
		sc.mtx.Unlock()
		pu := performanceUpdateFromProto(update.GetPerformanceUpdate())
		sc.callPairPerformanceUpdateListeners <- callPairPerformanceUpdateListenersReq{
			pair:      pair,
			update:    pu,
			listeners: listeners,
		}

	case *pbm.PairUpdateMessage_TrendlineUpdate:
		sc.mtx.Lock()
		listeners := make([]TrendlineUpdateCB, len(sc.pairTrendlineUpdateListeners))
		copy(listeners, sc.pairTrendlineUpdateListeners)
		sc.mtx.Unlock()
		tu := trendlineUpdateFromProto(update.GetTrendlineUpdate())
		sc.callPairTrendlineUpdateListeners <- callPairTrendlineUpdateListenersReq{
			pair:      pair,
			update:    tu,
			listeners: listeners,
		}
	}
}
