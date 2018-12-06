package wsclient

import (
	"strconv"
	"time"

	pbb "code.cryptowat.ch/ws-client-go/proto/broker"
	pbm "code.cryptowat.ch/ws-client-go/proto/markets"
	pbs "code.cryptowat.ch/ws-client-go/proto/stream"
	"github.com/golang/protobuf/proto"
	"github.com/juju/errors"
)

// TODO check if pointers are null

func marketFromProto(m *pbm.Market) Market {
	return Market{
		ID:             MarketID(uint64ToString(m.MarketId)),
		ExchangeID:     uint64ToString(m.ExchangeId),
		CurrencyPairID: uint64ToString(m.CurrencyPairId),
	}
}

func publicOrderFromProto(po *pbm.Order) PublicOrder {
	return PublicOrder{
		Price:  po.PriceStr,
		Amount: po.AmountStr,
	}
}

func orderBookSnapshotUpdateFromProto(obu *pbm.OrderBookUpdate) OrderBookSnapshotUpdate {
	bids := make([]PublicOrder, len(obu.Bids))
	for i, b := range obu.Bids {
		bids[i] = publicOrderFromProto(b)
	}

	asks := make([]PublicOrder, len(obu.Asks))
	for i, a := range obu.Asks {
		asks[i] = publicOrderFromProto(a)
	}

	return OrderBookSnapshotUpdate{
		SeqNum: SeqNum(obu.SeqNum),
		Bids:   bids,
		Asks:   asks,
	}
}

func orderBookDeltaUpdateFromProto(obdu *pbm.OrderBookDeltaUpdate) OrderBookDeltaUpdate {
	bidSet := make([]PublicOrder, len(obdu.Bids.Set))
	for i, o := range obdu.Bids.Set {
		bidSet[i] = publicOrderFromProto(o)
	}

	askSet := make([]PublicOrder, len(obdu.Asks.Set))
	for i, o := range obdu.Asks.Set {
		askSet[i] = publicOrderFromProto(o)
	}

	return OrderBookDeltaUpdate{
		SeqNum: SeqNum(obdu.SeqNum),

		Bids: OrderDeltas{
			Set:    bidSet,
			Remove: obdu.Bids.RemoveStr,
		},
		Asks: OrderDeltas{
			Set:    askSet,
			Remove: obdu.Asks.RemoveStr,
		},
	}
}

func orderBookSpreadUpdateFromProto(obsu *pbm.OrderBookSpreadUpdate) OrderBookSpreadUpdate {
	return OrderBookSpreadUpdate{
		Timestamp: time.Unix(obsu.Timestamp, 0),
		Bid:       publicOrderFromProto(obsu.Bid),
		Ask:       publicOrderFromProto(obsu.Ask),
	}
}

func tradesUpdateFromProto(tu *pbm.TradesUpdate) TradesUpdate {
	pt := make([]PublicTrade, len(tu.Trades))

	for i, t := range tu.Trades {
		var timestamp time.Time

		// Eliminate timestamp redundancy
		if t.TimestampNano > 0 {
			timestamp = time.Unix(0, t.TimestampNano)
		} else if t.TimestampMillis > 0 {
			timestamp = unixMillisToTime(t.TimestampMillis)
		} else {
			timestamp = time.Unix(t.Timestamp, 0)
		}

		pt[i] = PublicTrade{
			ExternalID: t.ExternalId,
			Timestamp:  timestamp,
			Price:      t.PriceStr,
			Amount:     t.AmountStr,
		}
	}

	return TradesUpdate{
		Trades: pt,
	}
}

func intervalsUpdateFromProto(iu *pbm.IntervalsUpdate) IntervalsUpdate {
	var is []Interval

	for _, i := range iu.Intervals {
		is = append(is, intervalFromProto(i))
	}

	return IntervalsUpdate{
		Intervals: is,
	}
}

func intervalFromProto(i *pbm.Interval) Interval {
	return Interval{
		CloseTime: time.Unix(i.Closetime, 0),
		Period:    Period(i.Period),
		OHLC: OHLC{
			Open:  i.Ohlc.OpenStr,
			High:  i.Ohlc.HighStr,
			Low:   i.Ohlc.LowStr,
			Close: i.Ohlc.CloseStr,
		},
		VolumeBase:  i.VolumeBaseStr,
		VolumeQuote: i.VolumeQuoteStr,
	}
}

func summaryUpdateFromProto(su *pbm.SummaryUpdate) SummaryUpdate {
	return SummaryUpdate{
		Last:           su.LastStr,
		High:           su.HighStr,
		Low:            su.LowStr,
		VolumeBase:     su.VolumeBaseStr,
		VolumeQuote:    su.VolumeQuoteStr,
		ChangeAbsolute: su.ChangeAbsoluteStr,
		ChangePercent:  su.ChangePercentStr,
		NumTrades:      su.NumTrades,
	}
}

func sparklineUpdateFromProto(su *pbm.SparklineUpdate) SparklineUpdate {
	return SparklineUpdate{
		Timestamp: time.Unix(su.Time, 0),
		Price:     su.PriceStr,
	}
}

func vwapUpdateFromProto(vu *pbm.PairVwapUpdate) VWAPUpdate {
	return VWAPUpdate{
		VWAP: float64ToString(vu.Vwap),
	}
}

func performanceUpdateFromProto(pu *pbm.PairPerformanceUpdate) PerformanceUpdate {
	return PerformanceUpdate{
		Window:      PerformanceWindow(pu.Window),
		Performance: float64ToString(pu.Performance),
	}
}

func trendlineUpdateFromProto(tu *pbm.PairTrendlineUpdate) TrendlineUpdate {
	return TrendlineUpdate{
		Window:    PerformanceWindow(tu.Window),
		Timestamp: time.Unix(tu.Time, 0),
		Price:     tu.Price,
		Volume:    tu.Volume,
	}
}

func privateOrderFromProto(order *pbb.PrivateOrder) PrivateOrder {
	var priceParams PriceParams
	for _, pp := range order.PriceParams {
		priceParams = append(priceParams, &PriceParam{
			Value: pp.ValueString,
			Type:  PriceParamType(pp.Type),
		})
	}

	if order.AmountFilledString == "" {
		order.AmountFilledString = "0.0"
	}

	return PrivateOrder{
		Timestamp:   time.Unix(order.Time, 0),
		PriceParams: priceParams,
		Amount:      order.AmountParamString,
		OrderSide:   OrderSide(order.Side),
		OrderType:   OrderType(order.Type),
		FundingType: FundingType(order.FundingType),
		ExpireTime:  time.Unix(order.ExpireTime, 0),

		ExternalID:   order.Id,
		Leverage:     order.Leverage,
		CurrentStop:  order.CurrentStopString,
		InitialStop:  order.InitialStopString,
		AmountFilled: order.AmountFilledString,
	}
}

func orderParamsToProto(o OrderParams) *pbb.PrivateOrder {
	priceParams := []*pbb.PrivateOrder_PriceParam{}
	for _, p := range o.PriceParams {
		priceParams = append(priceParams, &pbb.PrivateOrder_PriceParam{
			ValueString: p.Value,
			Type:        pbb.PrivateOrder_PriceParamType(p.Type),
		})
	}

	return &pbb.PrivateOrder{
		Side:              int32(o.OrderSide),
		Type:              pbb.PrivateOrder_Type(o.Type),
		FundingType:       pbb.FundingType(o.FundingType),
		PriceParams:       priceParams,
		AmountParamString: o.Amount,
		Leverage:          o.Leverage,
		ExpireTime:        timeToUnix(o.ExpireTime),
	}
}

func privateOrderToProto(o PrivateOrder) *pbb.PrivateOrder {
	priceParams := []*pbb.PrivateOrder_PriceParam{}
	for _, p := range o.PriceParams {
		priceParams = append(priceParams, &pbb.PrivateOrder_PriceParam{
			ValueString: p.Value,
			Type:        pbb.PrivateOrder_PriceParamType(p.Type),
		})
	}

	return &pbb.PrivateOrder{
		Id:          o.ExternalID,
		Side:        int32(o.OrderSide),
		Type:        pbb.PrivateOrder_Type(o.OrderType),
		FundingType: pbb.FundingType(o.FundingType),
		Time:        o.Timestamp.Unix(),
		PriceParams: priceParams,

		AmountParamString:  o.Amount,
		AmountFilledString: o.AmountFilled,
		ExpireTime:         timeToUnix(o.ExpireTime),
		Leverage:           o.Leverage,
	}
}

func balanceFromProto(balance *pbb.Balance) Balance {
	return Balance{
		Currency: balance.Currency,
		Amount:   balance.AmountString,
	}
}

func balancesToProto(balances Balances) []*pbb.Balances {
	var ret []*pbb.Balances

	for ftype, fbals := range balances {
		var balances []*pbb.Balance
		for _, bal := range fbals {
			balances = append(balances, &pbb.Balance{
				Currency:     bal.Currency,
				AmountString: bal.Amount,
			})
		}
		ret = append(ret, &pbb.Balances{
			FundingType: pbb.FundingType(ftype),
			Balances:    balances,
		})
	}

	return ret
}

func positionFromProto(position *pbb.PrivatePosition) PrivatePosition {
	return PrivatePosition{
		ExternalID: position.Id,
		Timestamp:  time.Unix(position.Time, 0),
		OrderSide:  OrderSide(position.Side),
		AvgPrice:   position.AvgPriceString,
		AmountOpen: position.AmountOpenString,
		OrderIDs:   position.OrderIds,
		TradeIDs:   position.TradeIds,
	}
}

func positionToProto(p PrivatePosition) *pbb.PrivatePosition {
	return &pbb.PrivatePosition{
		Id:                 p.ExternalID,
		Time:               p.Timestamp.Unix(),
		Side:               int32(p.OrderSide),
		AvgPriceString:     p.AvgPrice,
		AmountOpenString:   p.AmountOpen,
		AmountClosedString: p.AmountClosed,
		OrderIds:           p.OrderIDs,
		TradeIds:           p.TradeIDs,
	}
}

func tradeFromProto(trade *pbb.PrivateTrade) PrivateTrade {
	// Eliminate timestamp redundancy
	var t time.Time
	if trade.TimeMillis > 0 {
		t = unixMillisToTime(trade.TimeMillis)
	} else {
		t = time.Unix(trade.Time, 0)
	}

	return PrivateTrade{
		ExternalID: trade.ExternalId,
		OrderID:    trade.OrderId,
		Timestamp:  t,
		OrderSide:  OrderSide(trade.Side),
		Price:      trade.PriceString,
		Amount:     trade.AmountString,
	}
}

func tradeToProto(t PrivateTrade) *pbb.PrivateTrade {
	return &pbb.PrivateTrade{
		ExternalId:   t.ExternalID,
		OrderId:      t.OrderID,
		Time:         t.Timestamp.Unix(),
		TimeMillis:   t.Timestamp.UnixNano() / int64(time.Millisecond),
		PriceString:  t.Price,
		AmountString: t.Amount,
		Side:         int32(t.OrderSide),
	}
}

func unmarshalAuthnResultStream(data []byte) (*pbs.AuthenticationResult, error) {
	var msg pbs.StreamMessage

	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, errors.Trace(err)
	}

	authnResult := msg.GetAuthenticationResult()
	if authnResult == nil {
		return nil, errors.Trace(ErrInvalidAuthn)
	}

	return authnResult, nil
}

func unmarshalAuthnResultTrade(data []byte) (*pbs.AuthenticationResult, error) {
	var msg pbb.BrokerUpdateMessage

	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, errors.Trace(err)
	}

	authnResult := msg.GetAuthenticationResult()
	if authnResult == nil {
		return nil, errors.Trace(ErrInvalidAuthn)
	}

	return authnResult, nil
}

func unixMillisToTime(unixMillis int64) time.Time {
	secs := unixMillis / 1000
	ns := (unixMillis - secs*1000 /*ms*/) * 1000 /*ns*/
	return time.Unix(secs, ns)
}

// timeToUnix is like t.Unix(), but it returns 0 if t has zero value.
func timeToUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}

	return t.Unix()
}

func float32ToString(v float32) string {
	return strconv.FormatFloat(float64(v), 'f', -1, 32)
}

func float64ToString(v float64) string {
	return strconv.FormatFloat(float64(v), 'f', -1, 64)
}

func uint64ToString(u uint64) string {
	return strconv.FormatUint(u, 10)
}
