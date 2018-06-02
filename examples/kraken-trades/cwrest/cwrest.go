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

type exchangeDescrServer struct {
	Result ExchangeDescr `json:"result"`
}

type marketsDescrServer struct {
	Result []MarketDescr `json:"result"`
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
