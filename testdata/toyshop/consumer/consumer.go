// Package consumer holds the Kafka consumer entrypoint: a function looping
// over Reader.ReadMessage is itself the entrypoint for kafka-go.
package consumer

import (
	"context"

	kafka "github.com/segmentio/kafka-go"

	"example.com/toyshop/dyn"
	"example.com/toyshop/pkg/log"
)

const OrdersGroup = "toyshop-consumer"

func Run(ctx context.Context, brokers []string) error {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: OrdersGroup,
		Topic:   "orders.created",
	})
	defer r.Close()
	l := log.New()
	for {
		m, err := r.ReadMessage(ctx)
		if err != nil {
			return err
		}
		handle(l, m)
	}
}

func handle(l *log.Logger, m kafka.Message) {
	l.Info("consumed", "key", string(m.Key))
	dyn.Publish("order.consumed", string(m.Value))
}
