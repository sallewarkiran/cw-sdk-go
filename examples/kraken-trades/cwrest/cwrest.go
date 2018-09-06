package cwrest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/juju/errors"
)

type CWRESTClient struct {
	APIURL string
}

type ExchangeDescr struct {
	ID     int    `json:"id"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Routes struct {
		Markets string `json:"markets"`
	} `json:"routes"`
}

type MarketDescr struct {
	ID       int    `json:"id"`
	Exchange string `json:"exchange"`
	Pair     string `json:"pair"`
	Active   bool   `json:"active"`
	Route    string `json:"route"`
}

type PairDescr struct {
	ID     int    `json:"id"`
	Symbol string `json:"symbol"`

	Markets []MarketDescr
}

type OrderBook struct {
	Asks   [][]float32 `json:"asks"`
	Bids   [][]float32 `json:"bids"`
	SeqNum int         `json:"seqNum"`
}

type exchangeDescrServer struct {
	Result ExchangeDescr `json:"result"`
}

type marketsDescrServer struct {
	Result []MarketDescr `json:"result"`
}

type marketDescrServer struct {
	Result MarketDescr `json:"result"`
	Error  string      `json:"error"`
}

type pairDescrServer struct {
	Result PairDescr `json:"result"`
}

type orderbookServer struct {
	Result OrderBook `json:"result"`
}

func NewCWRESTClient(apiURL string) *CWRESTClient {
	return &CWRESTClient{
		APIURL: apiURL,
	}
}

func (c *CWRESTClient) GetExchangeDescr(exchangeSymbol string) (*ExchangeDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/exchanges/%s", c.APIURL, exchangeSymbol))
	if err != nil {
		return nil, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := exchangeDescrServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return nil, errors.Trace(err)
	}

	return &res.Result, nil
}

func (c *CWRESTClient) GetExchangeMarketsDescr(exchangeSymbol string) ([]MarketDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets/%s", c.APIURL, exchangeSymbol))
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

func (c *CWRESTClient) GetMarketsIndex() ([]MarketDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets", c.APIURL))
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

func (c *CWRESTClient) GetMarketDescr(exchangeSymbol string, pairSymbol string) (MarketDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets/%s/%s", c.APIURL, exchangeSymbol, pairSymbol))
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

func (c *CWRESTClient) GetPairDescr(symbol string) (PairDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/pairs/%s", c.APIURL, symbol))
	if err != nil {
		return PairDescr{}, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := pairDescrServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return PairDescr{}, errors.Trace(err)
	}

	return res.Result, nil
}

func (c *CWRESTClient) GetOrderBook(exchangeSymbol string, pairSymbol string) (OrderBook, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets/%s/%s/orderbook", c.APIURL, exchangeSymbol, pairSymbol))
	if err != nil {
		return OrderBook{}, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := orderbookServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return OrderBook{}, errors.Trace(err)
	}

	return res.Result, nil
}
