package pgln

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBuilder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, _ := pgxpool.New(ctx, os.Getenv("PGLN_CONNECTION_STRING"))
	defer pool.Close()
	builder := NewPGListenNotifyBuilder().
		SetContext(ctx).
		SetPool(pool)
	r, err := builder.Build()
	if err != nil {
		t.Error(err)
		return
	}
	defer r.Close()
	err = r.Listen("pgln_foo", ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			fmt.Printf("notification: %s - %s\n", channel, payload)
			cancel()
		},
		DoneCallback: func(channel string) {
			fmt.Printf("done: %s\n", channel)
		},
		ErrorCallback: func(channel string, err error) {
			if !strings.Contains(err.Error(), "context canceled") {
				fmt.Printf("error: %s - %s\n", channel, err)
				t.Error(err)
				cancel()
			}
		},
		OutOfSyncBlockingCallback: func(channel string) error {
			fmt.Printf("out-of-sync: %s\n", channel)
			err = r.Notify("pgln_foo", "working fine")
			if err != nil {
				t.Error(err)
				cancel()
			}
			return nil
		},
	})
	if err != nil {
		t.Error(err)
		return
	}

	select {
	case <-r.ctx.Done():
		return
	}
}
