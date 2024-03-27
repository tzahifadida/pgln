# pgln - A Postgresql Listen/Notify library that uses pgx "github.com/jackc/pgx/v5/pgxpool" as the underline driver

## Install

	go get github.com/tzahifadida/pgln

## Features

* Any connection string that pgxpool supports and you can set your custom pgxpool directly.
* Reconnects automatically
* Only 1 connection is held for all the Listen channels (notify acquires an additional connection, sends and releases)
* Have an out-of-sync callback for reconnects while holding a listener, so you can rebuild caches without losing notifications. 
* Notifications: `LISTEN`/`NOTIFY`

## Usage

See the builder_test.go for an example

## Tests

`go test` is used for testing. Please note that the connection string is provided as an environment variable: PGLN_CONNECTION_STRING

## Status

Because this library uses pgxpool as the underlying driver, you can contact pgx for any issue with the driver.
Feel free to contact for anything related to the pgln that is not related to the pgxpool driver.
Community members are encouraged to help each other with reported issues.