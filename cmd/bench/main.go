package main

import (
	"bench/benchpb"
	"context"
	"errors"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"strings"
	"time"
)

type benchEvent struct {
	ID   uint64 `db:"id"`
	Seq  uint64 `db:"seq"`
	Data string `db:"data"`
}

func transact(db *sqlx.DB, fn func(tx *sqlx.Tx) error) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	err = fn(tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func newBenchEventData() string {
	s := strings.Repeat("AB", 128)
	event := &benchpb.Event{
		Field1: s,
		Field2: s,
		Field3: s,
		Field4: s,
	}
	bytes, err := proto.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(bytes)
}

var benchEventData = newBenchEventData()

func main() {
	conn, err := grpc.Dial("localhost:10088", grpc.WithInsecure())
	if err != nil {
		panic(err)
	}
	client := benchpb.NewBenchServiceClient(conn)

	db := sqlx.MustConnect("mysql", "root:1@tcp(localhost:3306)/bench?parseTime=true")

	events := make([]benchEvent, 0, 1000)
	for i := 0; i < 1000; i++ {
		events = append(events, benchEvent{
			Data: benchEventData,
		})
	}

	fmt.Println("BEGIN:", time.Now())

	query := `
INSERT INTO events (data)
VALUES (:data)
`
	err = transact(db, func(tx *sqlx.Tx) error {
		res, err := db.NamedExec(query, events)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		num, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if int(num) != len(events) {
			return errors.New("wrong number of events inserted")
		}

		idVal := uint64(id)
		for i := range events {
			events[i].ID = idVal
			idVal++
		}

		_, err = db.NamedExec(`INSERT INTO event_seqs (id) VALUES (:id)`, events)
		return err
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("COMMIT:", time.Now())

	_, err = client.Signal(context.Background(), &benchpb.SignalRequest{})
	if err != nil {
		panic(err)
	}
	fmt.Println("SIGNALED:", time.Now())
}
