package main

import (
	"fmt"
	"github.com/iotaledger/iota.go/account"
	"github.com/iotaledger/iota.go/account/deposit"
	"github.com/iotaledger/iota.go/account/store"
	"github.com/iotaledger/iota.go/address"
	"github.com/iotaledger/iota.go/api"
	"github.com/iotaledger/iota.go/consts"
	"github.com/iotaledger/iota.go/trinary"
	"log"
	"math/rand"
	"net/url"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

const seed = "XRESYFHDADERN9FJEWHKIQQTIMHFU9UJIYCDCYCIZXCI9GLCLSZKNBPWQEAMOUSZXACZABHNYQDPREPNJ"

func main() {
	s := time.Now()
	iotaAPI, err := api.ComposeAPI(api.HTTPClientSettings{
		URI: "https://trinity.iota-tangle.io:14265",
	})
	must(err)

	// create a new store for our accounts (in-memory for testing purposes)
	store := store.NewInMemoryStore()
	options := &account.settings{
		SecurityLevel: consts.SecurityLevelLow, MWM: 14, Depth: 3,
	}

	// instantiate a new account with the underlying store and IRI API
	acc, err := account.NewAccount(seed, store, iotaAPI, options)
	must(err)

	// create a new deposit request which expires in 3 days
	threeDays := time.Now().AddDate(0, 0, 3)
	depositConditions, err := acc.NewDepositRequest(&deposit.Request{TimeoutAt: &threeDays})
	must(err)

	// construct a magnet link from the deposit conditions for depositors
	magnetLink := depositConditions.URL()
	iotaURL, err := url.Parse(magnetLink)
	must(err)

	// print out some information about the magnet link
	query := iotaURL.Query()
	fmt.Println("iota magnet link for deposit:", magnetLink)
	fmt.Println("address:", iotaURL.Host)
	fmt.Println("expires:", query.Get(deposit.ConditionExpires))
	fmt.Println("multi use:", query.Get(deposit.ConditionMultiUse))
	fmt.Println("expected amount:", query.Get(deposit.ConditionMultiUse))

	// send off N zero value transactions
	const txsToSendOff = 10

	// log all events inside the account
	confirmedSignal := make(chan struct{})
	logAccountEvents(acc, confirmedSignal, txsToSendOff)

	for i := 0; i < txsToSendOff; i++ {
		sendZeroTxToRandAddr(acc)
	}

	// block until all txs have been confirmed
	for range confirmedSignal {
	}
	fmt.Printf("all %d have been confirmed, exiting program...[%v]", txsToSendOff, time.Now().Sub(s))
}

const tryteAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ9"

// sends a zero value transaction to a random address
func sendZeroTxToRandAddr(acc *account.account) {
	randAddr := func() trinary.Hash {
		addr := ""
		for i := 0; i < 81; i++ {
			addr += string(tryteAlphabet[rand.Int()%len(tryteAlphabet)])
		}
		check, err := address.Checksum(addr)
		must(err)
		return addr + check
	}

	recipients := account.Recipients{}
	for i := 0; i < 1; i++ {
		recipients = append(recipients, account.Recipient{
			Address: randAddr(),
			Tag:     "BLABLABLA",
		})
	}

	bndl, err := acc.Send(recipients...)
	must(err)

	fmt.Print(bndl[0].Hash + "\n")
}

// logs all account happening inside the account
func logAccountEvents(acc *account.account, confirmedSignal chan struct{}, numTx int) {

	// create a new listener which listens on the given account events
	listener := account.ComposeEventListener(acc, account.EventPromotion, account.EventReattachment,
		account.EventSendingTransfer, account.EventTransferConfirmed, account.EventReceivedDeposit,
		account.EventReceivingDeposit, account.EventReceivedMessage)

	// keep track of how long it took for a transaction to get confirmed
	sendsStartTimes := map[string]time.Time{}

	// constantly log things happening in the account
	go func() {
		var counter int
	exit:
		for {
			select {
			case ev := <-listener.Promotions:
				log.Printf("promoted %s with %s\n", ev.BundleHash[:10], ev.PromotionTailTxHash)
			case ev := <-listener.Reattachments:
				log.Printf("reattached %s with %s\n", ev.BundleHash[:10], ev.ReattachmentTailTxHash)
			case ev := <-listener.Sending:
				tail := ev[0]
				log.Printf("sending %s with tail %s\n", tail.Bundle[:10], tail.Hash)
				sendsStartTimes[tail.Bundle] = time.Now()
			case ev := <-listener.Sent:
				tail := ev[0]
				start := sendsStartTimes[tail.Bundle]
				delta := time.Now().Sub(start)
				log.Printf("send (confirmed) %s with tail %s [%s]\n", tail.Bundle[:10], tail.Hash, delta)
				confirmedSignal <- struct{}{}
				counter++
				if counter == numTx {
					close(confirmedSignal)
					// terminate this logging goroutine
					break exit
				}
			case ev := <-listener.ReceivingDeposit:
				tail := ev[0]
				log.Printf("receiving deposit %s with tail %s\n", tail.Bundle[:10], tail.Hash)
			case ev := <-listener.ReceivedDeposit:
				tail := ev[0]
				log.Printf("received deposit %s with tail %s\n", tail.Bundle[:10], tail.Hash)
			case ev := <-listener.ReceivedMessage:
				tail := ev[0]
				log.Printf("received msg %s with tail %s\n", tail.Bundle[:10], tail.Hash)
			case errorEvent := <-acc.Errors():
				log.Printf("received error: %s\n", errorEvent.Error)
			}
		}
	}()
}