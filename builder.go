package pgln

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"time"
)

// PGListenNotify handles listening on notifying. It is generated by the Builder.
type PGListenNotify struct {
	connectionString  string
	reconnectInterval int
	pool              *pgxpool.Pool
	ctx               context.Context
	channels          map[string]*ListenOptions
	channelsChanged   bool
	lock              sync.RWMutex
	error             error
	cancelContextFunc context.CancelFunc
	ownChannel        string
}

type pgListenNotifyBuilder struct {
	r *PGListenNotify
}

type NotificationCallbackType func(channel string, payload string)
type DoneCallbackType func(channel string)
type ErrorCallbackType func(channel string, err error)
type OutOfSyncBlockingCallbackType func(channel string) error

// ListenOptions is used when Listen is called. We also internally maintain state in it.
type ListenOptions struct {
	// NotificationCallback is an optional callback that is called for each channel when a notification arrives
	NotificationCallback NotificationCallbackType
	// DoneCallback is an optional callback that is called for each channel when we are done listening.
	DoneCallback DoneCallbackType
	// ErrorCallback is an optional callback that is called for each channel when there is an unexpected error
	ErrorCallback ErrorCallbackType
	// OutOfSyncBlockingCallback is an optional BLOCKING callback that is called for each channel when we first connect
	// and when we reconnect. It is called just before the receiving the first notification.
	// It allows you to catch up or rebuild your caches while we are listening for new notifications so you do not lose your messages
	// while rebuilding.
	OutOfSyncBlockingCallback OutOfSyncBlockingCallbackType
	channel                   string
	executedListen            bool
	unListenRequested         bool
}

// PGListenNotifyBuilder builds a PGListenNotify structure you can use to listen for new notifications
type PGListenNotifyBuilder interface {
	// SetContext allows you to set your own custom context so we can stop listening when the context is done.
	SetContext(ctx context.Context) PGListenNotifyBuilder
	//UseConnectionString is a pgxpool supported connection. You must specify either a pool or a connection string.
	UseConnectionString(connectionString string) PGListenNotifyBuilder
	// SetPool allows you to set or reuse your custom pool. You must specify either a pool or a connection string.
	SetPool(pool *pgxpool.Pool) PGListenNotifyBuilder
	// SetReconnectInterval is an interval in milliseconds to wait between reconnection attempts.
	SetReconnectInterval(reconnectInterval int) PGListenNotifyBuilder
	// Build is the final call to finally build the PGListenNotify structure.
	Build() (*PGListenNotify, error)
}

func (rb *pgListenNotifyBuilder) SetReconnectInterval(reconnectInterval int) PGListenNotifyBuilder {
	if rb.r.error != nil {
		return rb
	}
	if reconnectInterval < 0 {
		rb.r.error = errors.New("reconnectInterval must be at least 0")
	}
	rb.r.reconnectInterval = reconnectInterval + 1
	return rb
}

func (rb *pgListenNotifyBuilder) UseConnectionString(connectionString string) PGListenNotifyBuilder {
	if rb.r.error != nil {
		return rb
	}
	rb.r.connectionString = connectionString
	if rb.r.pool != nil {
		rb.r.error = errors.New("please use either a connection string or set a pool")
	} else {
		ctx := context.Background()
		if rb.r.ctx != nil {
			ctx = rb.r.ctx
		}
		var err error
		rb.r.pool, err = pgxpool.New(ctx, connectionString)
		if err != nil {
			rb.r.error = errors.New("failed to create pool from connection string")
		}
	}
	return rb
}

func (rb *pgListenNotifyBuilder) SetContext(ctx context.Context) PGListenNotifyBuilder {
	if rb.r.error != nil {
		return rb
	}
	rb.r.ctx = ctx
	return rb
}

func (rb *pgListenNotifyBuilder) SetPool(pool *pgxpool.Pool) PGListenNotifyBuilder {
	if rb.r.error != nil {
		return rb
	}
	rb.r.pool = pool
	return rb
}

func (rb *pgListenNotifyBuilder) Build() (*PGListenNotify, error) {
	if rb.r.error != nil {
		return nil, rb.r.error
	}
	if rb.r.pool == nil {
		return nil, errors.New("pool cannot be null")
	}
	if rb.r.ctx == nil {
		return nil, errors.New("context cannot be null")
	}
	if rb.r.reconnectInterval == 0 {
		rb.r.reconnectInterval = 5000
	}
	randomUUID, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	rb.r.ownChannel = randomUUID.String()
	ctx, cancelFunc := context.WithCancel(rb.r.ctx)
	rb.r.ctx = ctx
	rb.r.cancelContextFunc = cancelFunc
	rb.r.channels = make(map[string]*ListenOptions)
	return rb.r, nil
}

// NewPGListenNotifyBuilder creates a new PGListenNotifyBuilder to configure the listener.
func NewPGListenNotifyBuilder() PGListenNotifyBuilder {
	return &pgListenNotifyBuilder{
		r: &PGListenNotify{
			pool: nil,
			ctx:  context.Background(),
		},
	}
}

// Close release resources that where created internally.
func (r *PGListenNotify) Close() {
	if r.cancelContextFunc != nil {
		defer r.cancelContextFunc()
	}
	if r.connectionString != "" && r.pool != nil {
		defer r.pool.Close()
	}
}

// UnListen stops listening for the channel.
func (r *PGListenNotify) UnListen(channel string) error {
	r.lock.Lock()
	defer r.lock.Unlock()
	options, ok := r.channels[channel]
	if !ok {
		return errors.New("could not locate channel: " + channel)
	}
	options.unListenRequested = true
	return nil
}

// Listen will "listen" to a channel specified in the options.
// to stop listening for a channel, use UnListen
func (r *PGListenNotify) Listen(channel string, options ListenOptions) error {
	optionsCopy := options
	optionsCopy.channel = channel
	r.lock.Lock()
	if len(r.channels) == 0 {
		r.startMonitoring()
	}
	r.channelsChanged = true
	r.channels[optionsCopy.channel] = &optionsCopy
	defer r.lock.Unlock()
	_ = r.Notify(r.ownChannel, "new listener")
	return nil
}

// Notify allows you to send a notification to a specified channel with a payload.
func (r *PGListenNotify) Notify(channel string, payload string) error {
	conn, err := r.pool.Acquire(r.ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(r.ctx, "select pg_notify($1,$2)", channel, payload)
	if err != nil {
		return err
	}
	return nil
}

func (r *PGListenNotify) startMonitoring() {
	waitForNotificationAndInitiateListens := func(conn *pgxpool.Conn) error {
		defer conn.Release()
		started := true
		for {
			var outOfSyncCalls []ListenOptions
			r.lock.RLock()
			channelsChanged := r.channelsChanged
			r.lock.RUnlock()
			if channelsChanged || started {
				var addChannels []string
				var removeChannels []string
				r.lock.Lock()
				r.channelsChanged = false
				for _, options := range r.channels {
					if options.unListenRequested {
						removeChannels = append(addChannels, options.channel)
					}
					if !options.executedListen || started {
						options.executedListen = true
						addChannels = append(addChannels, options.channel)
					}
				}
				for _, channel := range removeChannels {
					if r.channels[channel].DoneCallback != nil {
						go r.channels[channel].DoneCallback(channel)
					}
					delete(r.channels, channel)
				}
				for _, options := range r.channels {
					outOfSyncCalls = append(outOfSyncCalls, *options)
				}
				r.lock.Unlock()
				if started {
					_, err := conn.Exec(r.ctx, fmt.Sprintf("listen %s", pgx.Identifier{r.ownChannel}.Sanitize()))
					if err != nil {
						return err
					}
				}
				for _, channel := range addChannels {
					_, err := conn.Exec(r.ctx, fmt.Sprintf("listen %s", pgx.Identifier{channel}.Sanitize()))
					if err != nil {
						return err
					}
				}
				if !started {
					for _, channel := range removeChannels {
						_, err := conn.Exec(r.ctx, fmt.Sprintf("unlisten %s", pgx.Identifier{channel}.Sanitize()))
						if err != nil {
							return err
						}
					}
				}
			}

			select {
			case <-r.ctx.Done():
				return nil
			default:
			}
			if started && len(outOfSyncCalls) > 0 {
				for _, options := range outOfSyncCalls {
					if options.OutOfSyncBlockingCallback != nil && !options.unListenRequested {
						err := options.OutOfSyncBlockingCallback(options.channel)
						if err != nil {
							return err
						}
					}
				}
			}
			select {
			case <-r.ctx.Done():
				return nil
			default:
			}
			n, err := conn.Conn().WaitForNotification(r.ctx)
			if err != nil {
				return err
			}
			r.lock.RLock()
			for channel, options := range r.channels {
				if n.Channel == channel {
					if options.NotificationCallback != nil && !options.unListenRequested {
						go options.NotificationCallback(channel, n.Payload)
					}
				}
			}
			r.lock.RUnlock()
			started = false
		}
		return nil
	}
	go func() {
		// This is a fallback loop so that if we failed to release the wait when listening for a new channel, then after the interval it will release it.
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(time.Duration(10*1000) * time.Millisecond):
			}
			r.lock.RLock()
			channelsChanged := r.channelsChanged
			r.lock.RUnlock()
			if channelsChanged {
				_ = r.Notify(r.ownChannel, "new listener")
			}
		}
	}()
	go func() {
		acquireAndWait := func() {
			select {
			case <-r.ctx.Done():
				r.callDoneCallbacks()
				return
			default:
			}
			conn, err := r.pool.Acquire(r.ctx)
			if err != nil {
				r.lock.RLock()
				for channel, options := range r.channels {
					if options.ErrorCallback != nil && !options.unListenRequested {
						go options.ErrorCallback(channel, err)
					}
				}
				r.lock.RUnlock()
				return
			}
			defer conn.Release()
			err = waitForNotificationAndInitiateListens(conn)
			if err != nil {
				r.lock.RLock()
				for channel, options := range r.channels {
					if options.ErrorCallback != nil && !options.unListenRequested {
						go options.ErrorCallback(channel, err)
					}
				}
				r.lock.RUnlock()
				return
			}
		}
		for {
			acquireAndWait()
			select {
			case <-r.ctx.Done():
				r.callDoneCallbacks()
				return
			case <-time.After(time.Duration(r.reconnectInterval) * time.Millisecond):
			}
		}
	}()
}

func (r *PGListenNotify) callDoneCallbacks() {
	r.lock.RLock()
	for channel, options := range r.channels {
		if options.DoneCallback != nil {
			go options.DoneCallback(channel)
		}
	}
	r.lock.RUnlock()
}
