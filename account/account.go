package account

import (
	"crypto/sha256"
	"fmt"
	"github.com/iotaledger/iota.go/account/deposit"
	"github.com/iotaledger/iota.go/account/event"
	"github.com/iotaledger/iota.go/account/store"
	"github.com/iotaledger/iota.go/address"
	"github.com/iotaledger/iota.go/api"
	"github.com/iotaledger/iota.go/bundle"
	"github.com/iotaledger/iota.go/consts"
	"github.com/iotaledger/iota.go/guards"
	"github.com/iotaledger/iota.go/guards/validators"
	"github.com/iotaledger/iota.go/transaction"
	. "github.com/iotaledger/iota.go/trinary"
	"github.com/pkg/errors"
	"sync"
	"time"
)

// Account is a thread-safe object encapsulating address management, input selection, promotion and reattachments.
type Account interface {
	// ID returns the account's identifier.
	ID() string
	// Start starts the inner event loop of the account.
	Start() error
	// Shutdown cleanly shutdowns the account and releases its allocated resources.
	Shutdown() error
	// Send sends the specified amounts to the given recipients.
	Send(recipients ...Recipient) (bundle.Bundle, error)
	// AllocateDepositRequest generates a new deposit request.
	AllocateDepositRequest(req *deposit.Request) (*deposit.Conditions, error)
	// AvailableBalance gets the current available balance.
	// The balance is computed from all current deposit addresses which are ready
	// for input selection. To get the current total balance, use TotalBalance().
	AvailableBalance() (uint64, error)
	// TotalBalance gets the current total balance.
	// The total balance is computed from all currently allocated deposit addresses.
	// It does not represent the actual available balance for doing transfers.
	// Use AvailableBalance() to get the current available balance.
	TotalBalance() (uint64, error)
	// IsNew checks whether the account is new.
	IsNew() (bool, error)
	// UpdateSettings updates the settings of the account in a safe and synchronized manner.
	UpdateSettings(setts *Settings) error
}

// Recipient is a bundle.Transfer but with a nicer name.
type Recipient = bundle.Transfer
type Recipients []Recipient

// Sum returns the sum of all amounts.
func (recps Recipients) Sum() uint64 {
	var sum uint64
	for _, target := range recps {
		sum += target.Value
	}
	return sum
}

// AsTransfers converts the recipients to transfers.
func (recps Recipients) AsTransfers() bundle.Transfers {
	transfers := make(bundle.Transfers, len(recps))
	for i, recipient := range recps {
		transfers[i] = recipient
	}
	return transfers
}

// NewAccount creates a new account. If settings are nil, the account is
// initialized with the default settings provided by DefaultSettings().
func NewAccount(setts *Settings) (Account, error) {
	seed, err := setts.SeedProv.Seed()
	if err != nil {
		return nil, err
	}
	if err := validators.Validate(validators.ValidateSeed(seed)); err != nil {
		return nil, err
	}
	if setts == nil {
		setts = DefaultSettings()
	}
	return &account{
		id:    fmt.Sprintf("%x", sha256.Sum256([]byte(seed))),
		setts: setts,
	}, nil
}

type account struct {
	id string

	running bool

	// customization
	setts *Settings

	// sync
	mu sync.RWMutex

	// addr
	lastKeyIndex uint64
}

func (acc *account) ID() string {
	return acc.id
}

func (acc *account) Send(recipients ...Recipient) (bundle.Bundle, error) {
	acc.mu.Lock()
	defer acc.mu.Unlock()

	if !acc.running {
		return nil, ErrAccountNotRunning
	}

	if recipients == nil || len(recipients) == 0 {
		return nil, ErrEmptyRecipients
	}
	for _, target := range recipients {
		if !guards.IsTrytesOfExactLength(target.Address, consts.HashTrytesSize+consts.AddressChecksumTrytesSize) {
			return nil, consts.ErrInvalidAddress
		}
	}

	return acc.send(recipients)
}

func (acc *account) AllocateDepositRequest(req *deposit.Request) (*deposit.Conditions, error) {
	acc.mu.Lock()
	defer acc.mu.Unlock()

	if !acc.running {
		return nil, ErrAccountNotRunning
	}

	if req.TimeoutAt == nil {
		return nil, ErrTimeoutNotSpecified
	}

	currentTime, err := acc.setts.TimeSource.Time()
	if err != nil {
		return nil, err
	}

	if req.TimeoutAt.Add(-(time.Duration(5) * time.Minute)).Before(currentTime) {
		return nil, ErrTimeoutTooLow
	}

	return acc.allocateDepositRequest(req)
}

func (acc *account) AvailableBalance() (uint64, error) {
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	if !acc.running {
		return 0, ErrAccountNotRunning
	}
	return acc.availableBalance()
}

func (acc *account) TotalBalance() (uint64, error) {
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	if !acc.running {
		return 0, ErrAccountNotRunning
	}
	return acc.totalBalance()
}

func (acc *account) IsNew() (bool, error) {
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	if !acc.running {
		return false, ErrAccountNotRunning
	}
	state, err := acc.setts.Store.LoadAccount(acc.id)
	if err != nil {
		return false, err
	}
	return state.IsNew(), nil
}

func (acc *account) UpdateSettings(setts *Settings) error {
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if !acc.running {
		return ErrAccountNotRunning
	}
	// await all ongoing plugins to terminate
	if err := acc.shutdownPlugins(); err != nil {
		return errors.Wrap(err, "unable to shutdown plugin in update settings op.")
	}

	// make a copy
	settingsCopy := *setts
	acc.setts = &settingsCopy

	// continue polling goroutines
	if err := acc.startPlugins(); err != nil {
		return errors.Wrap(err, "unable to start plugin in update settings op.")
	}

	return nil
}

func (acc *account) Start() error {
	acc.mu.Lock()
	defer acc.mu.Unlock()
	// ensure account is known to the store
	state, err := acc.setts.Store.LoadAccount(acc.id)
	if err != nil {
		return errors.Wrap(err, "unable to read latest used key index in startup")
	}
	acc.lastKeyIndex = state.KeyIndex

	// start up plugins
	if err := acc.startPlugins(); err != nil {
		return err
	}

	acc.running = true
	return nil
}

func (acc *account) Shutdown() error {
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if !acc.running {
		return ErrAccountNotRunning
	}

	acc.running = false
	if err := acc.shutdownPlugins(); err != nil {
		return errors.Wrapf(err, "unable to shutdown plugin in shutdown op.")
	}

	acc.setts.EventMachine.Emit(struct{}{}, event.EventShutdown)
	return nil
}

func (acc *account) startPlugins() error {
	for _, p := range acc.setts.Plugins {
		if err := p.Start(acc); err != nil {
			return errors.Wrapf(err, "unable to start plugin %T", p)
		}
	}
	return nil
}

func (acc *account) shutdownPlugins() error {
	for _, p := range acc.setts.Plugins {
		if err := p.Shutdown(); err != nil {
			return errors.Wrapf(err, "unable to shutdown plugin %T", p)
		}
	}
	return nil
}

func (acc *account) allocateDepositRequest(req *deposit.Request) (*deposit.Conditions, error) {
	seed, err := acc.setts.SeedProv.Seed()
	if err != nil {
		return nil, err
	}

	acc.lastKeyIndex++
	addr, err := address.GenerateAddress(seed, acc.lastKeyIndex, acc.setts.SecurityLevel, true)
	if err != nil {
		return nil, errors.Wrap(err, "unable to generate address in address gen. function")
	}
	if err := acc.setts.Store.WriteIndex(acc.id, acc.lastKeyIndex); err != nil {
		return nil, errors.Wrapf(err, "unable to store next index (%d) in the store", acc.lastKeyIndex)
	}

	storedReq := &store.StoredDepositRequest{SecurityLevel: acc.setts.SecurityLevel, Request: *req}
	if err := acc.setts.Store.AddDepositRequest(acc.id, acc.lastKeyIndex, storedReq); err != nil {
		return nil, err
	}

	return &deposit.Conditions{Address: addr, Request: *req}, nil
}

func (acc *account) send(targets Recipients) (bundle.Bundle, error) {
	var inputs []api.Input
	var remainderAddress *Hash
	var err error
	transferSum := targets.Sum()
	forRemoval := []uint64{}
	var success bool

	seed, err := acc.setts.SeedProv.Seed()
	if err != nil {
		return nil, errors.Wrap(err, "unable to get seed from seed provider in send op.")
	}

	transfers := targets.AsTransfers()
	currentTime, err := acc.setts.TimeSource.Time()
	if err != nil {
		return nil, errors.Wrap(err, "unable to get current time in send op.")
	}

	// ensure that the allocated remainder address is deleted from the
	// store if the send operation wasn't successful.
	defer func() {
		if remainderAddress == nil || success {
			return
		}
		reqs, err := acc.setts.Store.GetDepositRequests(acc.id)
		if err != nil {
			return
		}
		// while iterating over all CDRs in order to find the one used for the remainder address is slow,
		// this operation only happens rarely, so there's no issue.
		croppedRemainderAddr := (*remainderAddress)[:81]
		var remainderAddrKeyIndex *uint64
		for keyIndex, req := range reqs {
			addr, err := address.GenerateAddress(seed, keyIndex, req.SecurityLevel, false)
			if err != nil {
				continue
			}
			if addr == croppedRemainderAddr {
				remainderAddrKeyIndex = &keyIndex
				break
			}
		}

		// shouldn't be possible
		if remainderAddrKeyIndex == nil {
			return
		}

		// remove allocated remainder address from store
		if err := acc.setts.Store.RemoveDepositRequest(acc.id, *remainderAddrKeyIndex); err != nil {
			err = errors.Wrap(err, "unable to cleanup allocated remainder addr during failed send op.")
			acc.setts.EventMachine.Emit(err, event.EventError)
		}
	}()

	if transferSum > 0 {
		// gather the total sum, inputs, addresses to remove from the store
		sum, ins, rem, err := acc.setts.InputSelectionStrat(acc, transferSum, false)
		if err != nil {
			return nil, errors.Wrap(err, "failed to perform input selection in send op.")
		}

		inputs = ins
		forRemoval = rem

		// store and add remainder address to transfer
		if sum > transferSum {
			remainder := sum - transferSum
			depCond, err := acc.allocateDepositRequest(&deposit.Request{ExpectedAmount: &remainder})
			if err != nil {
				return nil, errors.Wrap(err, "unable to generate remainder address in send op.")
			}
			remainderAddress = &depCond.Address
		}
	}

	ts := uint64(currentTime.UnixNano() / int64(time.Second))
	opts := api.PrepareTransfersOptions{
		Inputs:           inputs,
		RemainderAddress: remainderAddress,
		Timestamp:        &ts,
	}

	bundleTrytes, err := acc.setts.API.PrepareTransfers(seed, transfers, opts)
	if err != nil {
		return nil, errors.Wrap(err, "unable to prepare transfers in send op.")
	}

	tips, err := acc.setts.API.GetTransactionsToApprove(acc.setts.Depth)
	if err != nil {
		return nil, errors.Wrap(err, "unable to GTTA in send op.")
	}

	powedTrytes, err := acc.setts.API.AttachToTangle(tips.TrunkTransaction, tips.BranchTransaction, acc.setts.MWM, bundleTrytes)
	if err != nil {
		return nil, errors.Wrap(err, "performing PoW in send op. failed")
	}

	tailTx, err := transaction.AsTransactionObject(powedTrytes[0])
	if err != nil {
		return nil, err
	}

	// add the new transfer to the db
	if err := acc.setts.Store.AddPendingTransfer(acc.id, tailTx.Hash, powedTrytes, forRemoval...); err != nil {
		return nil, errors.Wrap(err, "unable to store pending transfer in send op.")
	}
	success = true

	bndlTrytes, err := acc.setts.API.StoreAndBroadcast(powedTrytes)
	if err != nil {
		return nil, errors.Wrap(err, "unable to store/broadcast bundle in send op.")
	}

	bndl, err := transaction.AsTransactionObjects(bndlTrytes, nil)
	if err != nil {
		return nil, err
	}

	acc.setts.EventMachine.Emit(bndl, event.EventSendingTransfer)
	return bndl, nil
}

func (acc *account) availableBalance() (uint64, error) {
	balance, _, _, err := acc.setts.InputSelectionStrat(acc, 0, true)
	return balance, err
}

func (acc *account) totalBalance() (uint64, error) {
	state, err := acc.setts.Store.LoadAccount(acc.id)
	if err != nil {
		return 0, errors.Wrap(err, "unable to load account state for querying total balance")
	}

	depositReqsCount := len(state.DepositRequests)
	if depositReqsCount == 0 {
		return 0, nil
	}

	solidSubtangleMilestone, err := acc.setts.API.GetLatestSolidSubtangleMilestone()
	if err != nil {
		return 0, errors.Wrap(err, "unable to fetch latest solid subtangle milestone for querying total balance")
	}
	subtangleHash := solidSubtangleMilestone.LatestSolidSubtangleMilestone

	seed, err := acc.setts.SeedProv.Seed()
	if err != nil {
		return 0, errors.Wrap(err, "unable to get seed from seed provider for computing total balance")
	}

	addrs := make(Hashes, len(state.DepositRequests))
	var i int
	for keyIndex, req := range state.DepositRequests {
		addr, _ := address.GenerateAddress(seed, keyIndex, req.SecurityLevel, true)
		addrs[i] = addr
		i++
	}

	balances, err := acc.setts.API.GetBalances(addrs, 100, subtangleHash)
	if err != nil {
		return 0, errors.Wrap(err, "unable to fetch balances for computing total balance")
	}
	var sum uint64
	for _, balance := range balances.Balances {
		sum += balance
	}

	return sum, nil
}

// selects fulfilled and timed out deposit addresses as inputs.
func defaultInputSelection(acc *account, transferValue uint64, balanceCheck bool) (uint64, []api.Input, []uint64, error) {
	depositRequests, err := acc.setts.Store.GetDepositRequests(acc.id)
	if err != nil {
		return 0, nil, nil, errors.Wrap(err, "unable to load account state for input selection")
	}

	// no deposit requests, therefore 0 balance
	if len(depositRequests) == 0 {
		if balanceCheck {
			return 0, nil, nil, nil
		}
		// we can't fulfill any transfer value if we have no deposit requests
		return 0, nil, nil, consts.ErrInsufficientBalance
	}

	// get the current solid subtangle milestone for doing each getBalance query with the same milestone
	solidSubtangleMilestone, err := acc.setts.API.GetLatestSolidSubtangleMilestone()
	if err != nil {
		return 0, nil, nil, errors.Wrap(err, "unable to fetch latest solid subtangle milestone for input selection")
	}
	subtangleHash := solidSubtangleMilestone.LatestSolidSubtangleMilestone

	// get current time to check for timed out addresses
	now, err := acc.setts.TimeSource.Time()
	if err != nil {
		return 0, nil, nil, errors.Wrap(err, "unable to get time for doing input selection")
	}

	type selection struct {
		keyIndex uint64
		req      *store.StoredDepositRequest
	}

	// primary addresses to use to try to use to fulfill the transfer value
	primaryAddrs := Hashes{}
	primarySelection := []selection{}

	// secondary addresses which are only used to fulfill the transfer
	// if the primary addresses couldn't fund the transfer.
	// the reason for this is that timed out addresses must be checked
	// for incoming consistent transfers, which is a slow operation.
	secondaryAddrs := Hashes{}
	secondarySelection := []selection{}

	// addresses/indices to remove from the store
	toRemove := []uint64{}

	markForRemoval := func(keyIndex uint64) {
		if balanceCheck {
			return
		}
		toRemove = append(toRemove, keyIndex)
	}

	seed, err := acc.setts.SeedProv.Seed()
	if err != nil {
		return 0, nil, nil, errors.Wrap(err, "unable to get seed from seed provider for doing input selection")
	}

	// iterate over all allocated deposit addresses
	for keyIndex, req := range depositRequests {
		// remainder address
		if req.TimeoutAt == nil {
			if req.ExpectedAmount == nil {
				panic("remainder address in system without 'expected amount'")
			}
			addr, _ := address.GenerateAddress(seed, keyIndex, req.SecurityLevel, true)
			primaryAddrs = append(primaryAddrs, addr)
			primarySelection = append(primarySelection, selection{keyIndex, req})
			continue
		}

		// timed out
		if now.After(*req.TimeoutAt) {
			addr, _ := address.GenerateAddress(seed, keyIndex, req.SecurityLevel, true)
			secondaryAddrs = append(secondaryAddrs, addr)
			secondarySelection = append(secondarySelection, selection{keyIndex, req})
			continue
		}

		// multi
		if req.MultiUse {
			// multi use deposit addresses are only used
			// when they are timed out, if they don't define an expected amount
			if req.ExpectedAmount == nil {
				continue
			}
			addr, _ := address.GenerateAddress(seed, keyIndex, req.SecurityLevel, true)
			primaryAddrs = append(primaryAddrs, addr)
			primarySelection = append(primarySelection, selection{keyIndex, req})
			continue
		}

		// single
		addr, _ := address.GenerateAddress(seed, keyIndex, req.SecurityLevel, true)
		primaryAddrs = append(primaryAddrs, addr)
		primarySelection = append(primarySelection, selection{keyIndex, req})
	}

	// get the balance of all addresses (also secondary) in one go
	toQuery := append(primaryAddrs, secondaryAddrs...)
	balances, err := acc.setts.API.GetBalances(toQuery, 100, subtangleHash)
	if err != nil {
		return 0, nil, nil, errors.Wrap(err, "unable to fetch balances of primary selected addresses for input selection")
	}

	inputs := []api.Input{}
	addAsInput := func(input *api.Input) {
		if balanceCheck {
			return
		}
		inputs = append(inputs, *input)
	}

	// add addresses as inputs which fulfill their criteria
	var sum uint64
	for i := range primarySelection {
		s := &primarySelection[i]
		// skip addresses which have an expected amount which isn't reached however
		if s.req.ExpectedAmount != nil && balances.Balances[i] < *s.req.ExpectedAmount {
			continue
		}
		sum += balances.Balances[i]

		// add the address as an input
		if balances.Balances[i] == 0 {
			continue
		}
		addAsInput(&api.Input{
			Address:  primaryAddrs[i],
			KeyIndex: s.keyIndex,
			Balance:  balances.Balances[i],
			Security: s.req.SecurityLevel,
		})

		// mark the address for removal as it should be freed from the store
		markForRemoval(s.keyIndex)
		if sum >= transferValue && !balanceCheck {
			break
		}
	}

	// if we didn't fulfill the transfer value,
	// lets use the timed out addresses too to try to fulfill the transfer
	if sum < transferValue || balanceCheck {
		startPosSecondary := len(primarySelection)

		for i := range secondarySelection {
			secSelect := &secondarySelection[i]
			addr := secondaryAddrs[i]

			balance := balances.Balances[startPosSecondary+i]

			// remove if there's no incoming consistent transfer
			// and the balance is zero in order free up the store
			if balance == 0 {
				// check whether the timed out address has an incoming consistent value transfer,
				// and if so, don't remove it from the store
				if has, err := acc.hasIncomingConsistentValueTransfer(addr); has || err != nil {
					continue
				}
				markForRemoval(secSelect.keyIndex)
				continue
			}
			markForRemoval(secSelect.keyIndex)
			sum += balance
			addAsInput(&api.Input{
				KeyIndex: secSelect.keyIndex,
				Address:  addr,
				Security: secSelect.req.SecurityLevel,
				Balance:  balance,
			})
			if sum >= transferValue && !balanceCheck {
				break
			}
		}
	}

	if balanceCheck {
		return sum, nil, nil, nil
	}

	if sum < transferValue {
		return 0, nil, nil, consts.ErrInsufficientBalance
	}
	return sum, inputs, toRemove, nil
}

func (acc *account) hasIncomingConsistentValueTransfer(addr Hash) (bool, error) {
	var has bool
	bndls, err := acc.setts.API.GetBundlesFromAddresses(Hashes{addr}, true)
	if err != nil {
		return false, err
	}
	persisted := map[string]struct{}{}
	for i := range bndls {
		if *(bndls[i][0]).Persistence {
			persisted[bndls[i][0].Bundle] = struct{}{}
			continue
		}

		// skip reattachments of an already persisted bundle
		if _, has := persisted[bndls[i][0].Bundle]; has {
			continue
		}

		// check whether it's even a deposit to the address we are checking
		var isDepositToAddr bool
		for j := range bndls[i] {
			if bndls[i][j].Value > 0 && bndls[i][j].Address == addr {
				isDepositToAddr = true
				break
			}
		}

		// ignore this transfer as it isn't an incoming value transfer
		if !isDepositToAddr {
			continue
		}

		// here we have a bundle which is not yet confirmed
		// and is depositing something onto this address.
		// lets check it for its consistency
		hash := bndls[i][0].Hash
		consistent, _, err := acc.setts.API.CheckConsistency(hash)
		if err != nil {
			return false, errors.Wrapf(err, "unable to check consistency of tx %s in incoming consistent transfer check", hash)
		}
		if consistent {
			has = true
			break
		}
	}
	return has, nil
}
