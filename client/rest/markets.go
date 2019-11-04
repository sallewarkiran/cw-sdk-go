package rest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/juju/errors"
)

type MarketDescr struct {
	ID       int    `json:"id"`
	Exchange string `json:"exchange"`
	Pair     string `json:"pair"`
	Active   bool   `json:"active"`
	Route    string `json:"route"`
}

type marketsDescrServer struct {
	Result []MarketDescr `json:"result"`
}

type marketDescrServer struct {
	Result MarketDescr `json:"result"`
	Error  string      `json:"error"`
}

func (c *RESTClient) GetMarketsIndex() ([]MarketDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets", c.baseURLStr))
	if err != nil {
		return nil, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := marketsDescrServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return nil, errors.Trace(err)
	}

	return res.Result, nil
}

func (c *RESTClient) GetMarketDescr(exchangeSymbol, pairSymbol string) (MarketDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets/%s/%s", c.baseURLStr, exchangeSymbol, pairSymbol))
	if err != nil {
		return MarketDescr{}, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := marketDescrServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return MarketDescr{}, errors.Trace(err)
	}

	if res.Error != "" {
		return MarketDescr{}, errors.New(res.Error)
	}

	return res.Result, nil
}

func (c *RESTClient) GetExchangeMarketsDescr(exchangeSymbol string) ([]MarketDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets/%s", c.baseURLStr, exchangeSymbol))
	if err != nil {
		return nil, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := marketsDescrServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return nil, errors.Trace(err)
	}

	return res.Result, nil
}
