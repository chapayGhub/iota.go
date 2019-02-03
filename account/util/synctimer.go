package util

import "time"

// SyncFunc is a function which is called in a synchronized manner.
type SyncFunc func() error

// OnErrorFunc is a function which is called when an error from the SyncFunc is returned.
type OnErrorFunc func(err error)

// NewSyncIntervalTimer creates a new SyncIntervalTimer with the given function and interval. onError can be nil.
func NewSyncIntervalTimer(interval time.Duration, f SyncFunc, onError OnErrorFunc) *SyncIntervalTimer {
	var intervalTimer *time.Timer
	if interval > 0 {
		intervalTimer = time.NewTimer(interval)
	}
	return &SyncIntervalTimer{
		f: f, onError: onError,
		intervalTimer: intervalTimer,
		sync:          make(chan struct{}),
		shutdown:      make(chan struct{}),
	}
}

// SyncIntervalTimer is a timer which executes a given function by the given interval.
type SyncIntervalTimer struct {
	f             SyncFunc
	onError       OnErrorFunc
	intervalTimer *time.Timer
	sync          chan struct{}
	shutdown      chan struct{}
}

// Start starts the timer loop. This function blocks the caller until
// the timer is stopped.
func (sit *SyncIntervalTimer) Start() {
	if sit.intervalTimer == nil {
	exitA:
		for {
			select {
			case sit.sync <- struct{}{}:
				<-sit.sync
			case <-sit.shutdown:
				break exitA
			}
		}
	} else {
	exitB:
		for {
			select {
			case <-sit.intervalTimer.C:
				if err := sit.f(); err != nil {
					if sit.onError != nil {
						sit.onError(err)
					}
				}
				select {
				case <-sit.shutdown:
					break exitB
				default:
				}
			case sit.sync <- struct{}{}:
				<-sit.sync
			case <-sit.shutdown:
				break exitB
			}
		}
	}
	close(sit.sync)
}

// Pause awaits the currently executing function to return and then pauses the interval loop.
func (sit *SyncIntervalTimer) Pause() {
	<-sit.sync
}

// Resume resumes the interval loop.
func (sit *SyncIntervalTimer) Resume() {
	sit.sync <- struct{}{}
}

// Stop awaits the currently executing function to return and then stops the interval loop.
func (sit *SyncIntervalTimer) Stop() {
	sit.shutdown <- struct{}{}
	if sit.intervalTimer != nil && !sit.intervalTimer.Stop() {
		<-sit.intervalTimer.C
	}
}
