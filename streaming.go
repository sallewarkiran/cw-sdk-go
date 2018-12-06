package wsclient

import (
	"time"
)

// MarketData is a container for all market data callbacks. For any MarketData
// intance, it will only ever have one of its properties non-null.
// See OnMarketData.
type MarketData struct {
	OrderBookSnapshotUpdate *OrderBookSnapshotUpdate
	OrderBookDeltaUpdate    *OrderBookDeltaUpdate
	OrderBookSpreadUpdate   *OrderBookSpreadUpdate
	TradesUpdate            *TradesUpdate
	IntervalsUpdate         *IntervalsUpdate
	SummaryUpdate           *SummaryUpdate
	SparklineUpdate         *SparklineUpdate
}

// PairData is a container for all pair data callbacks. For any PairData
// instance, it will only ever have one of its properties non-null.
// See OnPairData.
type PairData struct {
	VWAPUpdate        *VWAPUpdate
	PerformanceUpdate *PerformanceUpdate
	TrendlineUpdate   *TrendlineUpdate
}

// Market represents a market on Cryptowatch. A market consists of an exchange
// and currency pair. For example, Kraken BTC/USD. IDs are used instead of
// names to avoid inconsistencies associated with name changes.
//
// IDs for exchanges, currency pairs, and markets can be found through the
// following API URLs respectively:
// https://api.cryptowat.ch/exchanges
// https://api.cryptowat.ch/pairs
// https://api.cryptowat.ch/markets
type Market struct {
	ID             MarketID
	ExchangeID     string
	CurrencyPairID string
}

// PublicOrder represents a public order placed on an exchange. They often come
// as a slice of PublicOrder, which come with order book updates.
type PublicOrder struct {
	Price  string
	Amount string
}

// SeqNum is used to make sure order book deltas are processed in order. Each order book delta update has
// the sequence number incremented by 1. Sometimes sequence numbers reset to a smaller value (this happens when we deploy or
// restart our back end for whatever reason). In those cases, the first broadcasted update is a snapshot, so clients should
// not assume they are out of sync when they receive a snapshot with a lower seq number.
type SeqNum int32

// OrderBookSnapshotUpdate represents a full order book snapshot which comes with
// order book updates.
type OrderBookSnapshotUpdate struct {
	// SeqNum	is the sequence number of the last order book delta received.
	// Since snapshots are broadcast on a 1-minute interval regardless of updates,
	// it's possible this value doesn't change.
	// See the SeqNum definition for more information.
	SeqNum SeqNum

	Bids []PublicOrder
	Asks []PublicOrder
}

// OrderBookDeltaUpdate represents an order book delta update, which is
// the minimum amount of data necessary to keep a local order book up to date.
// Since order book snapshots are throttled at 1 per minute, subscribing to
// the delta updates is the best way to keep an order book up to date.
type OrderBookDeltaUpdate struct {
	// SeqNum is used to make sure deltas are processed in order.
	// See the SeqNum definition for more information.
	SeqNum SeqNum

	Bids OrderDeltas
	Asks OrderDeltas
}

// OrderDeltas are used to update an order book, either by setting (adding)
// new PublicOrders, or removing orders at specific prices.
type OrderDeltas struct {
	// Set is a list of orders used to add or replace orders on an order book.
	// Each order in Set is guaranteed to be a different price. For each of them,
	// if the order at that price exists on the book, replace it. If an order
	// at that price does not exist, add it to the book.
	Set []PublicOrder

	// Remove is a list of prices. To apply to an order book, remove all orders
	// of that price from that book.
	Remove []string
}

// OrderBookSpreadUpdate represents the most recent order book spread. It
// consists of the best current bid and as price.
type OrderBookSpreadUpdate struct {
	Timestamp time.Time
	Bid       PublicOrder
	Ask       PublicOrder
}

// TradesUpdate represents the most recent trades that have occurred for a
// particular market.
type TradesUpdate struct {
	Trades []PublicTrade
}

// PublicTrade represents a trade made on an exchange. See TradesUpdate and
// OnTradesUpdate.
type PublicTrade struct {
	// ID is given by the exchange, and not Cryptowatch.
	// NOTE some of these may be "0" since we still use
	// int64 on the back end to represent these IDs, and some are strings.
	ExternalID string
	Timestamp  time.Time
	Price      string
	Amount     string
}

// An interval represetns OHLC data for a particular Period and CloseTime.
type Interval struct {
	Period Period
	OHLC   OHLC

	// CloseTime is the time at which the Interval ended.
	CloseTime time.Time

	// VolumeBase is the amount of volume traded over this Interval, represented
	// in the base currency.
	VolumeBase string

	// VolumeQuote is the amount of volume traded over this Interval, represented
	// in the quote currency.
	VolumeQuote string
}

// Period is the number of seconds in an Interval.
type Period int32

// The following constants are all available Period values for IntervalsUpdate.
const (
	Period1M  Period = 60
	Period3M  Period = 180
	Period5M  Period = 300
	Period15M Period = 900
	Period30M Period = 1800
	Period1H  Period = 3600
	Period2H  Period = 7200
	Period4H  Period = 14400
	Period6H  Period = 21600
	Period12H Period = 43200
	Period1D  Period = 86400
	Period3D  Period = 259200
	Period1W  Period = 604800
)

// PeriodNames contains human-readable names for Period.
// e.g. Period1M = 60 (seconds) = "1m".
var PeriodNames = map[Period]string{
	Period1M:  "1m",
	Period3M:  "3m",
	Period5M:  "5m",
	Period15M: "15m",
	Period30M: "30m",
	Period1H:  "1h",
	Period2H:  "2h",
	Period4H:  "4h",
	Period6H:  "6h",
	Period12H: "12h",
	Period1D:  "1d",
	Period3D:  "3d",
	Period1W:  "1w",
}

// OHLC contains the open, low, high, and close prices for a given Interval.
type OHLC struct {
	Open  string
	High  string
	Low   string
	Close string
}

// IntervalsUpdate represents an update for OHLC data, at all relevant periods.
// Intervals is represented as a slice because more than one interval update
// can occur at the same time for different periods. For example, Period1M and
// Period3M will overlap every 3 minutes, so that IntervalsUpdate will contain
// both intervals.
type IntervalsUpdate struct {
	Intervals []Interval
}

// SummaryUpdate represents recent summary information for a particular market.
type SummaryUpdate struct {
	Last           string
	High           string
	Low            string
	VolumeBase     string
	VolumeQuote    string
	ChangeAbsolute string
	ChangePercent  string
	NumTrades      int32
}

// SparklineUpdate represents the sparkline update for a market at a particular time.
// A sparkline is a very small line chart, typically drawn without axes or coordinates.
// It presents the general shape of the variation in price.
// https://en.wikipedia.org/wiki/Sparkline
type SparklineUpdate struct {
	Timestamp time.Time
	Price     string
}

// Pair represents the currency pair as defined by the Cryptowatch API:
// https://api.cryptowat.ch/pairs
type Pair struct {
	ID string
}

// VWAPUpdate represents the most recent volume weighted average price update
// for a market.
// TODO explain calculation
// TODO does this need a timestamp?
type VWAPUpdate struct {
	VWAP string
}

// PerformanceWindow represents the time window over which performance is
// calculated.
type PerformanceWindow string

// The following constants represent every possible PerformanceWindow.
const (
	PerfWindow24h PerformanceWindow = "24h"
	PerfWindow1w  PerformanceWindow = "1w"
	PerfWindow1m  PerformanceWindow = "1m"
	PerfWindow3m  PerformanceWindow = "3m"
	PerfWindow6m  PerformanceWindow = "6m"
	PerfWindowYTD PerformanceWindow = "ytd"
	PerfWindow1y  PerformanceWindow = "1y"
	PerfWindow2y  PerformanceWindow = "2y"
	PerfWindow3y  PerformanceWindow = "3y"
	PerfWindow4y  PerformanceWindow = "4y"
	PerfWindow5y  PerformanceWindow = "5y"
)

// PerformanceUpdate represents the most recent performance update for a market
// over a particular window.
// TODO explain calculation
type PerformanceUpdate struct {
	Window      PerformanceWindow
	Performance string
}

// TrendlineUpdate represents the trendline update for a market for a particular
// window.
// TODO explain calculation
type TrendlineUpdate struct {
	Window    PerformanceWindow
	Timestamp time.Time
	Price     string
	Volume    string
}
