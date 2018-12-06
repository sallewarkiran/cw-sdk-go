// Copyright 2018 Cryptowatch. All Rights Reserved.
// Use of this source code is governed by a BSD-style
// license which can be found in the LICENSE file.

/*
Package wsclient manages connections to Cryptowatch Websocket API. The API consists of
two separate back ends for streaming and trading. Although they are separate services, both share
the same underlying connection logic. Each service has its own respective client: StreamClient and TradeClient.

Cryptowatch Websocket API

The Cryptowatch Websocket API is currently in Beta and is not yet publicly available. Please
inquire about getting access here: https://docs.google.com/forms/d/e/1FAIpQLSdhv_ceVtKA0qQcW6zQzBniRBaZ_cC4al31lDCeZirntkmWQw/viewform?c=0&w=1

Connecting

To connect to either the streaming or trading back end, you will need an API key pair. Please refer to the
section above about obtaining keys. The same API key pair will work for both streaming and trading, although
we use specific access control rules on our back end to determine if your key pair can access any particular
data subscription or trading functionality. In the future, you will be able to manage this through a settings
page on your Cryptowatch account.

Both websocket clients use the WSParams struct to connect to either service.

	type WSParams struct {
		// Required
		APIKey        string
		SecretKey     string
		Subscriptions []string

		// Not required
		URL           string
		ReconnectOpts *ReconnectOpts
	}

Typically you will only need to supply APIKey, SecretKey, and Subscriptions as the other parameters
have default values.

URL is the url of the back end to connect to. You will not need to supply it unless you are
testing against a non-production environment.

ReconnectOpts determine how (and if) the client should reconnect. By default, the client will
reconnect with linear backoff up to 30 seconds.

Subscriptions

The Subscriptions slice supplied in WSParams has a different meaning depending on whether you are
using the StreamClient or TradeClient.

In the context of StreamClient, Subscriptions are data subscriptions in the form of colon-separated
resources. For example, to get order book snapshot updates for a particular market, you might
supply "markets:68:book:snapshots". There is a limit of 100 subscriptions per connection.
Please refer to our Websocket API documentation to learn more about
the types of data subscriptions: https://cryptowat.ch/docs/streaming-api#intro.

Subscriptions for the TradeClient are simply the IDs of markets you wish to trade on. Market IDs can be
obtained through Cryptowatch's REST API: https://api.cryptowat.ch/markets.

NOTE: TradeClient currently supports 1 market at a time,
so you can only provide a single market ID to trade on. To trade on multiple markets, you should create
multiple TradeClient instances, each subscribing to a different market like "1".

We are adding support for multiple markets soon, and this will be available when ws-client-go is out of beta.

Basic Usage

The typical workflow is to create an instance of a client, set event handlers on it, then initiate the
connection. As events occur, the registered callbacks will be executed. For StreamClient, the callbacks
will contain data for the active subscriptions. For the TradeClient, callbacks will contain real-time
information about your open orders, trades, positions, or balances.

Stream Client

The stream client can be set up to process live market-level or pair-level data as follows:

	import "code.cryptowat.ch/ws-client-go"

	client, err := wsclient.NewStreamClient(&streamclient.WSParams{
		APIKey:    "myapikey",
		SecretKey: "mysecretkey",
		Subscriptions:    []string{
			"markets:86:trades", // Trade feed for Kraken BTCEUR
			"markets:87:trades", // Trade feed for Kraken BTCUSD
		},
	})
	if err != nil {
		log.Fatal("%s", err)
	}

	client.OnTradesUpdate(func(m wsclient.Market, tu wsclient.TradesUpdate) {
		// Do stuff with trades
	})

	// Set more handlers

	client.Connect()

Trade Client

The trade client maintains the state of your orders, trades, balances, and positions, while also
allowing you to place and cancel orders. In order to start trading, you must wait for the
internal cache to initialize, which can be accomplished using the OnReady callback.

	import "code.cryptowat.ch/ws-client-go"

	client, err := wsclient.NewTradeClient(&streamclient.WSParams{
		APIKey:    "myapikey",
		SecretKey: "mysecretkey",
		Subscriptions:    []string{
			"86", // Trade on Kraken BTCEUR
		},
	})
	if err != nil {
		log.Fatal("%s", err)
	}

	// Set handlers for orders, trades, positions, or balance updates

	client.OnReady(func() {
		// Now you can place/cancel orders
	})

	client.Connect()

Error Handling and Connection States

Both StreamClient and TradeClient can set error handlers using the OnError
method:

	var lastError error

	client.OnError(func(err error, disconnecting bool) {
    // Handle the error
	})

The "disconnecting" argument is set to true if the error is going to cause the
disconnection: in this case, the app could store the error somewhere and show
it later, when the actual disconnection happens; see the example of that below,
together with state listener. Error handlers are always called before the state
change listeners.

Both StreamClient and TradeClient can set listeners for connection state
changes such as StateDisconnected, StateWaitBeforeReconnect, StateConnecting,
StateAuthenticating, and StateEstablished.  They can also listen for any state
changes by using StateAny. The following is an example of how you would print
verbose logs about a client's state transitions. "client" can represent either
StreamClient or TradeClient:

	var lastError error

	client.OnError(func(err error, disconnecting bool) {
		// If the client is going to disconnect because of that error, just save
		// the error to print later with the disconnection message.
		if disconnecting {
			lastError = err
			return
		}

		// Otherwise, print the error message right away.
		log.Printf("Error: %s", err.Error())
	})

	client.OnStateChange(
		wsclient.ConnStateAny,
		func(oldState, state wsclient.ConnState) {
			causeStr := ""
			if lastError != nil {
				causeStr = fmt.Sprintf(" (%s)", lastError)
				lastError = nil
			}
			log.Printf(
				"State updated: %s -> %s%s",
				wsclient.ConnStateNames[oldState],
				wsclient.ConnStateNames[state],
				causeStr,
			)
		},
	)

Strings Vs Floats

All price, ID, and subscription data is represented as strings. This is to prevent loss of
significant digits from using floats, as well as to provide a consistent and simple API.

Concurrency

All methods of the StreamClient and TradeClient can be called concurrently from any
number of goroutines. All callbacks and listeners are called by the same internal goroutine,
unique to each connection; that is, they are never called concurrently with each other.

Stream Client CLI

Use the command line tool stream-client to subscribe to live data feeds from the command line. To
install the tool, run "make" from the root of the repo. This will create the executable
bin/stream-client, which can be used as follows:

	./stream-client \
		-apikey=your_api_key \
		-secretkey=your_secret_key \
		-sub subscription_key \
		-sub subscription_key2

*/
package wsclient
