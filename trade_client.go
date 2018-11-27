package wsclient

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	pbb "code.cryptowat.ch/ws-client-go/proto/broker"
	"code.cryptowat.ch/ws-client-go/internal"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

const (
	defaultTradeURL = "wss://broker.cryptowat.ch"
	requestTimeout  = 10 * time.Second
	tradeCacheLimit = 1000
)

// The following errors are returned from TradeClient.
var (
	// ErrNotInitialized is returned when PlaceOrder or CancelOrder is called before the client is initialized.
	// This indicates you have not waited until OnReady was called, or the client is in a disconnected state.
	ErrNotInitialized = errors.New("trade client not initialized")

	// ErrInvalidOrder is returned from PlaceOrder if the order is not valid. Not valid in this case
	// means the order was likely not placed correctly on the exchange.
	ErrInvalidOrder = errors.New("order is not valid")

	// ErrNoExchangeAccess is returned from the OnError callback when the Cryptowatch trading back end
	// does not have proper permissions to access the relevant exchange's API. Usually this means you
	// need to add your API keys to the relevant exchange at https://cryptowat.ch/account/api-keys.
	ErrNoExchangeAccess = errors.New("cryptowatch account is missing exchange api keys")

	// ErrBadProto is returned from PlaceOrder or CancelOrder if the Cryptowatch trading back end returns
	// invalid protobuf data. This should never happen.
	ErrBadProto = errors.New("request is not a valid proto type")
)

type sessionStatusUpdateCB func(marketID MarketID, update *pbb.SessionStatusUpdate)

// OrdersUpdateCB defines a callback function for OnOrdersUpdate.
type OrdersUpdateCB func(marketID MarketID, orders []PrivateOrder)

// PrivateTradesUpdateCB defines a callback function for OnTradesUpdate.
type PrivateTradesUpdateCB func(marketID MarketID, trades []PrivateTrade)

// OnPositionsUpdateCB defines a callback function for OnPositionsUpdate.
type OnPositionsUpdateCB func(marketID MarketID, positions []PrivatePosition)

// OnBalancesUpdateCB defines a callback function for OnBalancesUpdate.
type OnBalancesUpdateCB func(marketID MarketID, balances Balances)

type OnMarketReadyCB func(marketID MarketID, ready bool)

type OnErrorCB func(marketID MarketID, err error)

type callOrdersListenersReq struct {
	marketID  MarketID
	update    []PrivateOrder
	listeners []OrdersUpdateCB
}

type callTradesListenersReq struct {
	marketID  MarketID
	update    []PrivateTrade
	listeners []PrivateTradesUpdateCB
}

type callBalancesListenersReq struct {
	marketID  MarketID
	update    Balances
	listeners []OnBalancesUpdateCB
}

type callPositionsListenersReq struct {
	marketID  MarketID
	update    []PrivatePosition
	listeners []OnPositionsUpdateCB
}

type callSessionStatusListenersReq struct {
	marketID  MarketID
	update    *pbb.SessionStatusUpdate
	listeners []sessionStatusUpdateCB
}

type callMarketReadyListenersReq struct {
	marketID  MarketID
	ready     bool
	listeners []OnMarketReadyCB
}

type placeOrderResp struct {
	order PrivateOrder
	err   error
}

type placeOrderReq struct {
	marketID    MarketID
	orderParams OrderParams
	response    chan placeOrderResp
}

type cancelOrderReq struct {
	marketID MarketID
	orderID  string
	response chan error
}

// TradeClient is used to manage a connection to Cryptowatch's trading back end, and
// provides functions for trading on the the subscribed markets. TradeClient
// also has callbacks for updates related to trading.
// NOTE multiple markets will be enabled in a future update.
type TradeClient struct {
	// marketIDs is a slice of all market IDs this trade client cares about right
	// now.
	marketIDs []MarketID

	// Internal cache used for resolving responses to specific ws messages
	requests map[string]chan *pbb.RequestResolutionUpdate

	// This is the current state of the session which matches the broker backend
	// all values are kept up to date internally.
	orders    map[MarketID]map[string]PrivateOrder
	trades    map[MarketID][]PrivateTrade
	positions map[MarketID]map[string]PrivatePosition
	balances  map[MarketID]Balances

	placeOrderRequests  chan placeOrderReq
	cancelOrderRequests chan cancelOrderReq

	callOrdersListeners        chan callOrdersListenersReq
	callTradesListeners        chan callTradesListenersReq
	callBalancesListeners      chan callBalancesListenersReq
	callPositionsListeners     chan callPositionsListenersReq
	callSessionStatusListeners chan callSessionStatusListenersReq

	ordersListeners      []OrdersUpdateCB
	tradesListeners      []PrivateTradesUpdateCB
	balancesListeners    []OnBalancesUpdateCB
	positionsListeners   []OnPositionsUpdateCB
	marketReadyListeners []OnMarketReadyCB

	sessionStatusListeners []sessionStatusUpdateCB
	onReadyCallbacks       []func()
	OnErrorCBs             []OnErrorCB

	disable chan struct{}

	*wsConn
	mtx sync.Mutex
}

// NewTradeClient creates a new TradeClient based on the given WSParams. Subscriptions
// should be an array of market IDs that you have access to trade on.
// NOTE multiple markets will be enabled in a future update.
func NewTradeClient(params *WSParams) (*TradeClient, error) {
	wsConn, err := newWsConn(params)
	if err != nil {
		return nil, errors.Trace(err)
	}

	marketIDs := make([]MarketID, 0, len(params.Subscriptions))
	for _, s := range params.Subscriptions {
		marketIDs = append(marketIDs, MarketID(s))
	}

	tc := &TradeClient{
		wsConn:    wsConn,
		marketIDs: marketIDs,

		// Used to signal shutdown from a lost connection
		disable: make(chan struct{}, 1),

		// Internal request cache
		requests: make(map[string]chan *pbb.RequestResolutionUpdate),

		orders:    make(map[MarketID]map[string]PrivateOrder),
		trades:    make(map[MarketID][]PrivateTrade),
		positions: make(map[MarketID]map[string]PrivatePosition),
		balances:  make(map[MarketID]Balances, FundingTypeCnt),

		placeOrderRequests:  make(chan placeOrderReq, 1),
		cancelOrderRequests: make(chan cancelOrderReq, 1),

		callOrdersListeners:        make(chan callOrdersListenersReq, 1),
		callTradesListeners:        make(chan callTradesListenersReq, 1),
		callBalancesListeners:      make(chan callBalancesListenersReq, 1),
		callPositionsListeners:     make(chan callPositionsListenersReq, 1),
		callSessionStatusListeners: make(chan callSessionStatusListenersReq, 1),
	}

	tc.wsConn.transport.OnRead(func(conn *internal.StreamTransportConn, data []byte) {
		var msg pbb.BrokerUpdateMessage

		if err := proto.Unmarshal(data, &msg); err != nil {
			// Failed to parse incoming message: close connection (and if
			// reconnection was requested, then reconnect)
			conn.CloseOpt(
				websocket.FormatCloseMessage(websocket.CloseUnsupportedData, ""),
				false,
			)
			return
		}

		switch msg.Update.(type) {

		case *pbb.BrokerUpdateMessage_AuthenticationResult:
			wsConn.authHandler(msg.GetAuthenticationResult())

			// Exposed
		case *pbb.BrokerUpdateMessage_OrdersUpdate:
			tc.ordersUpdateHandler(tc.sMarketID(msg.GetMarketId()), msg.GetOrdersUpdate())

		case *pbb.BrokerUpdateMessage_TradesUpdate:
			tc.tradesUpdateHandler(tc.sMarketID(msg.GetMarketId()), msg.GetTradesUpdate())

		case *pbb.BrokerUpdateMessage_BalancesUpdate:
			tc.balancesUpdateHandler(tc.sMarketID(msg.GetMarketId()), msg.GetBalancesUpdate())

		case *pbb.BrokerUpdateMessage_PositionsUpdate:
			tc.positionsUpdateHandler(tc.sMarketID(msg.GetMarketId()), msg.GetPositionsUpdate())

		case *pbb.BrokerUpdateMessage_SessionStatusUpdate:
			tc.sessionStatusUpdateHandler(tc.sMarketID(msg.GetMarketId()), msg.GetSessionStatusUpdate())

			// Internal order resolution
		case *pbb.BrokerUpdateMessage_RequestResolutionUpdate:
			tc.requestResolutionHandler(tc.sMarketID(msg.GetMarketId()), msg.GetRequestResolutionUpdate())

		case *pbb.BrokerUpdateMessage_ApiAccessorStatusUpdate:
			// We're interested in this update to figure whether we have access to
			// the exchange.
			tc.apiAccessorStatusUpdateHandler(tc.sMarketID(msg.GetMarketId()), msg.GetApiAccessorStatusUpdate())

		case *pbb.BrokerUpdateMessage_PermissionsUpdate:
			// TODO there may be error states in here we should handle
			// fmt.Println(msg.GetPermissionsUpdate())

		case *pbb.BrokerUpdateMessage_AnonymousSessionStatusUpdate:
			// Not used

		default:
			// Not a supported type
		}
	})

	tc.OnConnClosed(func(_ ConnState, _ error) {
		tc.disable <- struct{}{}
	})

	go tc.listen()

	return tc, nil
}

// This serves as the main program logic. It runs in its own goroutine and
// handles order requests as well as dispatching listener callbacks. This
// design asllows the client to place/cancel as many orders at a time that
// they want while retaininc a synchronous api.
func (tc *TradeClient) listen() {
	tradeStatus := newTradeStatusTracker()

	marketReady := make(map[MarketID]bool, len(tc.marketIDs))
	allMarketsReady := false

listenloop:
	for {
		select {
		case req := <-tc.placeOrderRequests:
			if !marketReady[req.marketID] {
				req.response <- placeOrderResp{
					err: ErrNotInitialized,
				}
				continue
			}

			go func() {
				req.response <- tc.placeOrderInt(req.marketID, req.orderParams)
			}()

		case req := <-tc.cancelOrderRequests:
			if !marketReady[req.marketID] {
				req.response <- ErrNotInitialized
				continue
			}

			go func() {
				req.response <- tc.cancelOrderInt(req.marketID, req.orderID)
			}()

		case req := <-tc.callOrdersListeners:
			tradeStatus.setModuleReady(req.marketID, tradeModuleOrders)
			for _, l := range req.listeners {
				l(req.marketID, req.update)
			}

		case req := <-tc.callTradesListeners:
			tradeStatus.setModuleReady(req.marketID, tradeModuleTrades)
			for _, l := range req.listeners {
				l(req.marketID, req.update)
			}

		case req := <-tc.callBalancesListeners:
			tradeStatus.setModuleReady(req.marketID, tradeModuleBalances)
			for _, l := range req.listeners {
				l(req.marketID, req.update)
			}

		case req := <-tc.callPositionsListeners:
			tradeStatus.setModuleReady(req.marketID, tradeModulePositions)
			for _, l := range req.listeners {
				l(req.marketID, req.update)
			}

		case req := <-tc.callSessionStatusListeners:
			tradeStatus.setModuleReady(req.marketID, tradeModuleSession)
			for _, l := range req.listeners {
				l(req.marketID, req.update)
			}

		case <-tc.disable:
			tradeStatus.reset()
		}

		someIsntReady := false

		for _, marketID := range tc.marketIDs {
			curMarketReady := tradeStatus.isMarketReady(marketID)
			if curMarketReady != marketReady[marketID] {
				marketReady[marketID] = curMarketReady

				// Call market-ready listeners
				tc.mtx.Lock()
				cbs := make([]OnMarketReadyCB, len(tc.marketReadyListeners))

				copy(cbs, tc.marketReadyListeners)
				tc.mtx.Unlock()

				for _, cb := range cbs {
					cb(marketID, curMarketReady)
				}
			}

			if !curMarketReady {
				someIsntReady = true
			}
		}

		if someIsntReady {
			allMarketsReady = false
			continue listenloop
		}

		if !allMarketsReady {
			allMarketsReady = true
			cbs := []func(){}

			tc.mtx.Lock()
			cbs = append(cbs, tc.onReadyCallbacks...)
			tc.mtx.Unlock()

			for _, cb := range cbs {
				cb()
			}
		}
	}
}

func (tc *TradeClient) makeRequest(marketID MarketID, req interface{}) (*pbb.RequestResolutionUpdate, error) {
	requestID := uuid.New().String()
	responseChan := make(chan *pbb.RequestResolutionUpdate, 1)
	requestType := "unknown"

	tc.mtx.Lock()
	tc.requests[requestID] = responseChan
	tc.mtx.Unlock()

	defer func() {
		tc.mtx.Lock()
		delete(tc.requests, requestID)
		tc.mtx.Unlock()
	}()

	var marketIDInt int64
	if marketID != "" {
		var err error
		marketIDInt, err = strconv.ParseInt(string(marketID), 10, 64)
		if err != nil {
			return nil, errors.Annotatef(err, "invalid market id %q", marketID)
		}
	}

	var sendErr error

	switch r := req.(type) {
	case *pbb.BrokerRequest_PlaceOrderRequest:
		requestType = "place-order"
		sendErr = tc.sendProto(context.Background(), &pbb.BrokerRequest{
			Id:       requestID,
			MarketId: marketIDInt,
			Request:  r,
		})

	case *pbb.BrokerRequest_CancelOrderRequest:
		requestType = "cancel-order"
		sendErr = tc.sendProto(context.Background(), &pbb.BrokerRequest{
			Id:       requestID,
			MarketId: marketIDInt,
			Request:  r,
		})

	default:
		return nil, ErrBadProto
	}

	if sendErr != nil {
		return nil, errors.Annotate(sendErr, "tradeclient makeRequest failed")
	}

	select {
	case r := <-responseChan:
		return r, nil

	case <-time.After(requestTimeout):
		// TODO Do we delete the request here? Or wait for it to possibly resolve?
		return nil, fmt.Errorf("request timed out (%v); Type=%v;", requestTimeout, requestType)
	}
}

func (tc *TradeClient) apiAccessorStatusUpdateHandler(marketID MarketID, res *pbb.APIAccessorStatusUpdate) {
	if !res.HasAccess {
		// Cryptowatch account doesn't have access to the exchange, so report that
		// to the user.
		//
		// TODO: when we have multiple sessions per connection, create a dedicated
		// type for that kind of error, so that clients can figure which exchange
		// we don't have access to.

		tc.mtx.Lock()
		cbs := make([]OnErrorCB, len(tc.OnErrorCBs))

		copy(cbs, tc.OnErrorCBs)
		tc.mtx.Unlock()

		for _, cb := range cbs {
			cb(marketID, ErrNoExchangeAccess)
		}
	}
}

func (tc *TradeClient) requestResolutionHandler(marketID MarketID, res *pbb.RequestResolutionUpdate) {
	tc.mtx.Lock()
	resultChan, ok := tc.requests[res.Id]
	tc.mtx.Unlock()

	if !ok {
		// This means the server double-sent a request resolution update. This should
		// never happen, but we can ignore anyway
		return
	}

	resultChan <- res
}

// PlaceOrder creates a new order based on the given OrderParams. PlaceOrder blocks
// until the order has been placed on the exchange or an error occurs. PlaceOrder
// can be called concurrently as many times as needed.
func (tc *TradeClient) PlaceOrder(marketID MarketID, o OrderParams) (PrivateOrder, error) {
	response := make(chan placeOrderResp, 1)

	tc.placeOrderRequests <- placeOrderReq{
		marketID:    marketID,
		orderParams: o,
		response:    response,
	}

	res := <-response

	return res.order, res.err
}

func (tc *TradeClient) placeOrderInt(marketID MarketID, o OrderParams) placeOrderResp {
	res, err := tc.makeRequest(
		marketID,
		&pbb.BrokerRequest_PlaceOrderRequest{
			PlaceOrderRequest: &pbb.PlaceOrderRequest{
				Order: orderParamsToProto(o),
			},
		},
	)
	if err != nil {
		return placeOrderResp{
			order: PrivateOrder{Error: 500},
			err:   errors.Annotate(err, "request failed: place-order"),
		}
	}

	var (
		order     PrivateOrder
		returnErr error
	)

	if res.Error == 0 {
		order = privateOrderFromProto(res.GetPlaceOrderResult().Order)
		tc.mtx.Lock()
		tc.orders[marketID][order.ExternalID] = order
		tc.mtx.Unlock()
	} else {
		returnErr = fmt.Errorf("[%v] %v", res.Error, res.Message)
		order = PrivateOrder{Error: res.Error}
	}

	return placeOrderResp{
		order: order,
		err:   returnErr,
	}
}

// CancelOrder cancels the given order on the exchange. CancelOrder blocks
// until the order has been placed or if an error occurs. it can be called
// concurrently on as many different orders as needed.
func (tc *TradeClient) CancelOrder(marketID MarketID, order PrivateOrder) error {
	if order.ExternalID == "" {
		return ErrInvalidOrder
	}

	response := make(chan error, 1)

	tc.cancelOrderRequests <- cancelOrderReq{
		marketID: marketID,
		orderID:  order.ExternalID,
		response: response,
	}

	return <-response
}

func (tc *TradeClient) cancelOrderInt(marketID MarketID, orderID string) error {
	res, err := tc.makeRequest(
		marketID,
		&pbb.BrokerRequest_CancelOrderRequest{
			CancelOrderRequest: &pbb.CancelOrderRequest{
				OrderId: orderID,
			},
		})

	if err != nil {
		return errors.Annotate(err, "request failed: cancel-order")
	}

	if res.Error != 0 {
		return fmt.Errorf("[%v] %v", res.Error, res.Message)
	}

	return nil
}

// Sync forces a cache update by polling the exchange on behalf of the user.
// This function should not normally be needed, and is only useful in two scenarios:
// 1) an order is placed or cancelled outside of this client.
// 2) there is something preventing our trading back end from actively polling for updates.
// This happens rarely, and for various reasons. For example, an exchange may rate limit
// one of our servers.
func (tc *TradeClient) Sync(marketID MarketID) error {
	var marketIDInt int64
	if marketID != "" {
		var err error
		marketIDInt, err = strconv.ParseInt(string(marketID), 10, 64)
		if err != nil {
			return errors.Annotatef(err, "invalid market id %q", marketID)
		}
	}

	tc.sendProto(context.Background(), &pbb.BrokerRequest{
		MarketId: marketIDInt,
		Request: &pbb.BrokerRequest_SyncRequest{
			SyncRequest: &pbb.SyncRequest{},
		},
	})

	return nil
}

// GetOrders returns the list of current open orders ordered by execution time (oldest first).
func (tc *TradeClient) GetOrders(marketID MarketID) []PrivateOrder {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	var ods []PrivateOrder
	for _, order := range tc.orders[marketID] {
		ods = append(ods, order)
	}

	sort.Sort(privateOrders(ods))

	return ods
}

// GetTrades returns the 1000 most recent trades ordered by execution time (oldest first).
// NOTE the api for this will change once multiple markets are supported per connection.
func (tc *TradeClient) GetTrades(marketID MarketID) []PrivateTrade {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tds := make([]PrivateTrade, len(tc.trades[marketID]))
	copy(tds, tc.trades[marketID])

	sort.Sort(privateTrades(tds))

	return tds
}

// GetPositions returns the list of open positions ordered by execution time (oldest first).
// NOTE the api for this will change once multiple markets are supported per connection.
func (tc *TradeClient) GetPositions(marketID MarketID) []PrivatePosition {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	var ps []PrivatePosition
	for _, position := range tc.positions[marketID] {
		ps = append(ps, position)
	}

	sort.Sort(privatePositions(ps))

	return ps
}

// GetBalances returns a map of FundingType to a list of balances for a particular exchange.
// NOTE the api for this will change once multiple markets are supported per connection.
func (tc *TradeClient) GetBalances(marketID MarketID) Balances {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	balances := make(Balances, FundingTypeCnt)

	for ftype, bals := range tc.balances[marketID] {
		balsCopy := make([]Balance, len(bals))
		copy(balsCopy, bals)
		balances[ftype] = balsCopy
	}

	return balances
}

func (tc *TradeClient) ordersUpdateHandler(marketID MarketID, update *pbb.OrdersUpdate) {
	orders := update.GetOrders()
	var orderCache = make(map[string]PrivateOrder, len(orders))
	var returnOrders []PrivateOrder

	for _, order := range orders {
		o := privateOrderFromProto(order)
		orderCache[o.ExternalID] = o
		returnOrders = append(returnOrders, o)
	}

	tc.mtx.Lock()

	// Update internal order cache and call order update listeners
	tc.orders[marketID] = orderCache

	// Handle listeners
	listeners := make([]OrdersUpdateCB, len(tc.ordersListeners))
	copy(listeners, tc.ordersListeners)

	tc.mtx.Unlock()

	tc.callOrdersListeners <- callOrdersListenersReq{
		marketID:  marketID,
		update:    returnOrders,
		listeners: listeners,
	}
}

func (tc *TradeClient) tradesUpdateHandler(marketID MarketID, update *pbb.TradesUpdate) {
	trades := update.GetTrades()
	var newTrades = make([]PrivateTrade, 0, len(trades))

	for _, trade := range trades {
		newTrades = append(newTrades, tradeFromProto(trade))
	}

	tc.mtx.Lock()

	tradeSlice := tc.trades[marketID]

	tradeSlice = append(tradeSlice, newTrades...)

	// Make sure the trade cache doesn't exceed tradeCacheLimit by removing
	// oldest trades.
	if len(tradeSlice) > tradeCacheLimit {
		tradeSlice = tradeSlice[(len(tradeSlice) - tradeCacheLimit):]
	}

	tc.trades[marketID] = tradeSlice

	listeners := make([]PrivateTradesUpdateCB, len(tc.tradesListeners))
	copy(listeners, tc.tradesListeners)

	tc.mtx.Unlock()

	tc.callTradesListeners <- callTradesListenersReq{
		marketID:  marketID,
		update:    newTrades,
		listeners: listeners,
	}
}

func (tc *TradeClient) balancesUpdateHandler(marketID MarketID, update *pbb.BalancesUpdate) {
	balancesCache := make(Balances, FundingTypeCnt)

	// Converting the proto structure, array of arrays, to map of arrays
	// keyed on FundingType
	for _, fundingBalances := range update.GetBalances() {
		var fbals []Balance
		for _, b := range fundingBalances.Balances {
			fbals = append(fbals, balanceFromProto(b))
		}
		balancesCache[FundingType(fundingBalances.FundingType)] = fbals
	}

	tc.mtx.Lock()

	tc.balances[marketID] = balancesCache

	listeners := make([]OnBalancesUpdateCB, len(tc.balancesListeners))
	copy(listeners, tc.balancesListeners)

	tc.mtx.Unlock()

	tc.callBalancesListeners <- callBalancesListenersReq{
		marketID:  marketID,
		update:    balancesCache,
		listeners: listeners,
	}
}

func (tc *TradeClient) positionsUpdateHandler(marketID MarketID, update *pbb.PositionsUpdate) {
	positions := update.GetPositions()
	var positionsCache = make(map[string]PrivatePosition, len(positions))
	var returnPositions []PrivatePosition

	for _, position := range positions {
		p := positionFromProto(position)
		positionsCache[p.ExternalID] = p
		returnPositions = append(returnPositions, p)
	}

	tc.mtx.Lock()

	tc.positions[marketID] = positionsCache

	listeners := make([]OnPositionsUpdateCB, len(tc.positionsListeners))
	copy(listeners, tc.positionsListeners)

	tc.mtx.Unlock()

	tc.callPositionsListeners <- callPositionsListenersReq{
		marketID:  marketID,
		update:    returnPositions,
		listeners: listeners,
	}
}

func (tc *TradeClient) sessionStatusUpdateHandler(marketID MarketID, update *pbb.SessionStatusUpdate) {
	tc.mtx.Lock()
	listeners := make([]sessionStatusUpdateCB, len(tc.sessionStatusListeners))
	copy(listeners, tc.sessionStatusListeners)
	tc.mtx.Unlock()

	tc.callSessionStatusListeners <- callSessionStatusListenersReq{
		marketID:  marketID,
		update:    update,
		listeners: listeners,
	}
}

// OnReady sets a callback function for when the client is fully initialized and ready to place/cancel trades.
// PlaceOrder and CancelOrder will return ErrNotInitialized if called before OnReady is called. OnReady is called
// each time the client initializes, meaning if the client disconnects for whatever reason, and then reconnects,
// it will be called again. Any number of OnReady callbacks can be placed, and can be done so safely from multiple
// goroutines.
func (tc *TradeClient) OnReady(cb func()) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.onReadyCallbacks = append(tc.onReadyCallbacks, cb)
}

// OnError sets a callback function for errors associated with the trading client or trading back end.
// TODO move this to wsConn, make it work for StreamClient and TradeClient
func (tc *TradeClient) OnError(cb OnErrorCB) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.OnErrorCBs = append(tc.OnErrorCBs, cb)
}

// OnOrdersUpdate sets a callback for order updates. This will be called
// immediately when the client initializes, and for every subsequent order
// update. Each time this callback is executed, the entire list of open orders
// is returned (including partially filled orders).
func (tc *TradeClient) OnOrdersUpdate(cb OrdersUpdateCB) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.ordersListeners = append(tc.ordersListeners, cb)
}

// OnTradesUpdate sets a callabck for trade updates. This will be called
// immediately when the client initializes with the 1000 most recent trades.
// For every subsequent update, it will contain only the most recent trades.
func (tc *TradeClient) OnTradesUpdate(cb PrivateTradesUpdateCB) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.tradesListeners = append(tc.tradesListeners, cb)
}

// OnBalancesUpdate sets a callback for balance updates. This will be called
// immediately when the client initializes, and for every subsequent balance
// update. An internal cache of balances is kept, and can be accessed with
// GetBalances()
func (tc *TradeClient) OnBalancesUpdate(cb OnBalancesUpdateCB) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.balancesListeners = append(tc.balancesListeners, cb)
}

// OnPositionsUpdate sets a callback for position updates. This will be
// called immediately when the client initializes, and for every subsequent
// position update. An internal cache of positions is kept, and can be accessed
// with GetPositions().
func (tc *TradeClient) OnPositionsUpdate(cb OnPositionsUpdateCB) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.positionsListeners = append(tc.positionsListeners, cb)
}

func (tc *TradeClient) OnMarketReadyChange(cb OnMarketReadyCB) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.marketReadyListeners = append(tc.marketReadyListeners, cb)
}

// TODO This is unused and may not be necessary. we just need the listener to
// be aware of the status.
func (tc *TradeClient) onSessionStatusUpdate(cb sessionStatusUpdateCB) {
	tc.mtx.Lock()
	defer tc.mtx.Unlock()

	tc.sessionStatusListeners = append(tc.sessionStatusListeners, cb)
}

func (tc *TradeClient) sMarketID(marketID int64) MarketID {
	// TODO: remove it when the new broker is deployed
	if marketID == 0 {
		// This can happen if we try to connect to the old broker, so just use
		// the first subscription
		return tc.marketIDs[0]
	}

	return MarketID(fmt.Sprintf("%d", marketID))
}

// tradeStatusTracker {{{
type tradeStatusTracker struct {
	m map[MarketID]map[tradeModule]bool
}

func newTradeStatusTracker() *tradeStatusTracker {
	return &tradeStatusTracker{
		m: map[MarketID]map[tradeModule]bool{},
	}
}

func (tst *tradeStatusTracker) setModuleReady(marketID MarketID, module tradeModule) {
	if _, ok := tst.m[marketID]; !ok {
		tst.m[marketID] = map[tradeModule]bool{}
	}

	tst.m[marketID][module] = true
}

func (tst *tradeStatusTracker) reset() {
	tst.m = map[MarketID]map[tradeModule]bool{}
}

func (tst *tradeStatusTracker) isMarketReady(marketID MarketID) bool {
	for _, module := range tradeModules {
		if !tst.m[marketID][module] {
			return false
		}
	}

	return true
}

// }}}
