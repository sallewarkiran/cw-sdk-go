package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	pbm "code.cryptowat.ch/stream-client-go/proto/markets"
	"code.cryptowat.ch/stream-client-go/examples/kraken-trades/cwrest"
	"github.com/fatih/color"
)

var (
	red     = color.RedString
	yellow  = color.YellowString
	magenta = color.MagentaString
	green   = color.GreenString
	blue    = color.BlueString
	cyan    = color.CyanString
)

// PrettyDeltaUpdate wraps the protobufOrderBookDeltaUpdate message and provides
// a String method which formats the update in a nice way.
type PrettyDeltaUpdate struct {
	*pbm.OrderBookDeltaUpdate
}

// PrettyDeltaUpdate's String method formats the pbm.OrderBookDeltaUpdate
// message to colored, human-readable text.
func (pdu PrettyDeltaUpdate) String() string {
	deltaUpdate := pdu.OrderBookDeltaUpdate

	type order struct {
		price  string
		amount float32
	}

	askSets := []order{}
	for _, ask := range deltaUpdate.GetAsks().GetSet() {
		askSets = append(askSets, order{red(ask.GetPriceStr()), ask.GetAmount()})
	}
	askDeltas := []order{}
	for _, ask := range deltaUpdate.GetAsks().GetDelta() {
		askDeltas = append(askDeltas, order{yellow(ask.GetPriceStr()), ask.GetAmount()})
	}
	askRemoves := []string{}
	for _, ask := range deltaUpdate.GetAsks().GetRemoveStr() {
		askRemoves = append(askRemoves, magenta(ask))
	}

	bidSets := []order{}
	for _, bid := range deltaUpdate.GetBids().GetSet() {
		bidSets = append(bidSets, order{green(bid.GetPriceStr()), bid.GetAmount()})
	}
	bidDeltas := []order{}
	for _, bid := range deltaUpdate.GetBids().GetDelta() {
		bidDeltas = append(bidDeltas, order{blue(bid.GetPriceStr()), bid.GetAmount()})
	}
	bidRemoves := []string{}
	for _, bid := range deltaUpdate.GetBids().GetRemoveStr() {
		bidRemoves = append(bidRemoves, cyan(bid))
	}

	rows := []string{
		fmt.Sprintf("%s / %s / %s / %s / %s / %s --- SeqNum: %d",
			red("Set Ask"), yellow("Delta Ask"), magenta("Remove Ask"),
			green("Set Bid"), blue("Delta Bid"), cyan("Remove Bid"),
			deltaUpdate.GetSeqNum()),
		fmt.Sprintf("%s %v", red("====="), askSets),
		fmt.Sprintf("%s %v", yellow("+++++"), askDeltas),
		fmt.Sprintf("%s %v", magenta("-----"), askRemoves),
		fmt.Sprintf("%s %v", green("====="), bidSets),
		fmt.Sprintf("%s %v", blue("+++++"), bidDeltas),
		fmt.Sprintf("%s %v", cyan("-----"), bidRemoves),
	}
	return strings.Join(rows, "\n")
}

// OrderBookMirror is meant to hold an accurate reconstruction of the
// orderbook for a particular market as long as it's updated with up-to-date
// consecutive delta messages.
type OrderBookMirror struct {
	Asks   map[string]float32
	Bids   map[string]float32
	SeqNum int32
}

// NewOrderBookMirror builds an OrderBookMirror instance from a cwrest
// GetOrderBook response.
func NewOrderBookMirror(bookMsg cwrest.OrderBook) *OrderBookMirror {
	book := &OrderBookMirror{
		Asks:   map[string]float32{},
		Bids:   map[string]float32{},
		SeqNum: int32(bookMsg.SeqNum),
	}

	for _, order := range bookMsg.Asks {
		price, amount := order[0], order[1]
		book.Asks[strconv.FormatFloat(float64(price), 'f', -1, 32)] = amount
	}
	for _, order := range bookMsg.Bids {
		price, amount := order[0], order[1]
		book.Bids[strconv.FormatFloat(float64(price), 'f', -1, 32)] = amount
	}

	return book
}

// Update applies the pbm.OrderBookDeltaUpdate messages to keep
// OrderBookMirror up-to-date.
// Update returns an error if the there's a gap in sequence numbers.
func (book *OrderBookMirror) Update(deltaUpdate *pbm.OrderBookDeltaUpdate) error {

	// Check and update sequence number
	if deltaUpdate.GetSeqNum()-book.SeqNum != 1 {
		return fmt.Errorf("trying to update OrderBook (SeqNum: %d) with "+
			"OrderBookDeltaUpdate (SeqNum: %d)", book.SeqNum,
			deltaUpdate.GetSeqNum())
	}
	book.SeqNum = deltaUpdate.GetSeqNum()

	// Update asks
	for _, ask := range deltaUpdate.GetAsks().GetDelta() {
		book.Asks[ask.GetPriceStr()] += ask.GetAmount()
	}
	for _, ask := range deltaUpdate.GetAsks().GetRemoveStr() {
		delete(book.Asks, ask)
	}
	for _, ask := range deltaUpdate.GetAsks().GetSet() {
		book.Asks[ask.GetPriceStr()] = ask.GetAmount()
	}

	// Update bids
	for _, bid := range deltaUpdate.GetBids().GetDelta() {
		book.Bids[bid.GetPriceStr()] += bid.GetAmount()
	}
	for _, bid := range deltaUpdate.GetBids().GetRemoveStr() {
		delete(book.Bids, bid)
	}
	for _, bid := range deltaUpdate.GetBids().GetSet() {
		book.Bids[bid.GetPriceStr()] = bid.GetAmount()
	}

	return nil
}

// PrettyString formats the orderbook to colored, human-readable text.
func (book *OrderBookMirror) PrettyString() string {
	type order struct {
		price  string
		amount float32
	}

	asks := []order{}
	for price, amount := range book.Asks {
		asks = append(asks, order{red(price), amount})
	}
	sort.Slice(asks, func(i, j int) bool {
		return asks[i].price > asks[j].price
	})

	bids := []order{}
	for price, amount := range book.Bids {
		bids = append(bids, order{green(price), amount})
	}
	sort.Slice(bids, func(i, j int) bool {
		// reverse order for bids
		return bids[i].price > bids[j].price
	})

	return fmt.Sprintf("%s / %s --- SeqNum: %d\n%v\n%v", red("Asks"), green("Bids"), book.SeqNum, asks, bids)
}
