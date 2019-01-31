package builder

import (
	"github.com/iotaledger/iota.go/account"
	"github.com/iotaledger/iota.go/account/event"
	"github.com/iotaledger/iota.go/account/plugins/promoter"
	"github.com/iotaledger/iota.go/account/plugins/transfer/poller"
	"github.com/iotaledger/iota.go/account/store"
	"github.com/iotaledger/iota.go/account/timesrc"
	"github.com/iotaledger/iota.go/api"
	"github.com/iotaledger/iota.go/consts"
	. "github.com/iotaledger/iota.go/trinary"
	"time"
)

// NewBuilder creates a new Builder which uses the default settings
// provided by DefaultSettings().
func NewBuilder() *Builder {
	setts := account.DefaultSettings()
	setts.Plugins = make(map[string]account.Plugin)
	return &Builder{setts}
}

// Builder wraps a Settings object and provides a builder pattern around it.
type Builder struct {
	settings *account.Settings
}

// Build creates the account from the given settings.
func (b *Builder) Build() (account.Account, error) {
	settsCopy := *b.settings
	return account.NewAccount(&settsCopy)
}

// Settings returns the currently built settings.
func (b *Builder) Settings() (*account.Settings) {
	settsCopy := *b.settings
	return &settsCopy
}

// API sets the underlying API to use.
func (b *Builder) WithAPI(api *api.API) *Builder {
	b.settings.API = api
	return b
}

// Store sets the underlying store to use.
func (b *Builder) WithStore(store store.Store) *Builder {
	b.settings.Store = store
	return b
}

// SeedProvider sets the underlying SeedProvider to use.
func (b *Builder) WithSeedProvider(seedProv account.SeedProvider) *Builder {
	b.settings.SeedProv = seedProv
	return b
}

// Seed sets the underlying seed to use.
func (b *Builder) WithSeed(seed Trytes) *Builder {
	b.settings.SeedProv = account.NewInMemorySeedProvider(seed)
	return b
}

// MWM sets the minimum weight magnitude used to send transactions.
func (b *Builder) WithMWM(mwm uint64) *Builder {
	b.settings.MWM = mwm
	return b
}

// Depth sets the depth used when searching for transactions to approve.
func (b *Builder) WithDepth(depth uint64) *Builder {
	b.settings.Depth = depth
	return b
}

// The overall security level used by the account.
func (b *Builder) WithSecurityLevel(level consts.SecurityLevel) *Builder {
	b.settings.SecurityLevel = level
	return b
}

// TimeSource sets the TimeSource to use to get the current time.
func (b *Builder) WithTimeSource(timesource timesrc.TimeSource) *Builder {
	b.settings.TimeSource = timesource
	return b
}

// InputSelectionFunc sets the strategy to determine inputs and usable balance.
func (b *Builder) WithInputSelectionStrategy(strat account.InputSelectionFunc) *Builder {
	b.settings.InputSelectionStrat = strat
	return b
}

// WithDefaultPlugins adds a transfer poller and promoter-reattacher plugin with following settings:
//
// poll incoming/outgoing transfers every 30 seconds (filter by tail tx hash).
// promote/reattach each pending transfer every 30 seconds.
//
// This function should only be called after following settings are initialized:
// api, store, mwm, depth, event machine, time source, seed provider.
func (b *Builder) WithDefaultPlugins() *Builder {
	transferPoller := poller.NewTransferPoller(
		b.settings.API, b.settings.Store, b.settings.EventMachine,
		b.settings.SeedProv, poller.NewPerTailReceiveEventFilter(true),
		time.Duration(30)*time.Second,
	)
	promoterReattacher := promoter.NewPromoter(
		b.settings.API, b.settings.Store, b.settings.EventMachine, b.settings.TimeSource,
		time.Duration(30)*time.Second,
		b.settings.Depth, b.settings.MWM)

	b.settings.Plugins[transferPoller.Name()] = transferPoller
	b.settings.Plugins[promoterReattacher.Name()] = promoterReattacher
	return b
}

// WithPlugins adds the given plugins to use.
func (b *Builder) WithPlugins(plugins ...account.Plugin) *Builder {
	for _, p := range plugins {
		b.settings.Plugins[p.Name()] = p
	}
	return b
}

// WithEvents instructs the account to emit events using the given EventMachine.
func (b *Builder) WithEvents(em event.EventMachine) *Builder {
	b.settings.EventMachine = em
	return b
}
