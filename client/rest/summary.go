package rest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/juju/errors"
	"github.com/shopspring/decimal"

	"code.cryptowat.ch/cw-sdk-go/common"
)

type marketSummariesServer struct {
	Result map[string]marketSummariesItemServer `json:"result"`
}

type marketSummariesItemServer struct {
	Price struct {
		Last   decimal.Decimal `json:"last"`
		High   decimal.Decimal `json:"high"`
		Low    decimal.Decimal `json:"low"`
		Change struct {
			Percentage decimal.Decimal `json:"percentage"`
			Absolute   decimal.Decimal `json:"absolute"`
		} `json:"change"`
	} `json:"price"`
	Volume      decimal.Decimal `json:"volume"`
	VolumeQuote decimal.Decimal `json:"volumeQuote"`
}

type marketSummaryServer struct {
	Result marketSummariesItemServer `json:"result"`
}

func (c *RESTClient) GetSummary(
	exchangeSymbol, pairSymbol string,
) (common.SummaryUpdate, error) {
	resp, err := http.Get(fmt.Sprintf(
		"%s/markets/%s/%s/summary",
		c.baseURLStr, exchangeSymbol, pairSymbol,
	))
	if err != nil {
		return common.SummaryUpdate{}, errors.Trace(err)
	}

	defer resp.Body.Close()

	srv := marketSummaryServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&srv); err != nil {
		return common.SummaryUpdate{}, errors.Trace(err)
	}

	ret := marketSummariesItemServerToSummaryUpdate(srv.Result)

	return ret, nil
}

// GetMarketSummaries returns a map from market symbol like "<exchange>:<pair>"
// to the corresponding summary.
func (c *RESTClient) GetMarketSummaries() (map[string]common.SummaryUpdate, error) {
	resp, err := http.Get(fmt.Sprintf(
		"%s/markets/summaries", c.baseURLStr,
	))
	if err != nil {
		return nil, errors.Trace(err)
	}

	defer resp.Body.Close()

	srv := marketSummariesServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&srv); err != nil {
		return nil, errors.Trace(err)
	}

	ret := make(map[string]common.SummaryUpdate, len(srv.Result))
	for market, v := range srv.Result {
		ret[market] = marketSummariesItemServerToSummaryUpdate(v)
	}

	return ret, nil
}

func marketSummariesItemServerToSummaryUpdate(v marketSummariesItemServer) common.SummaryUpdate {
	return common.SummaryUpdate{
		Last:           v.Price.Last,
		High:           v.Price.High,
		Low:            v.Price.Low,
		ChangeAbsolute: v.Price.Change.Absolute,
		ChangePercent:  v.Price.Change.Percentage,
		VolumeBase:     v.Volume,
		VolumeQuote:    v.VolumeQuote,
	}
}
