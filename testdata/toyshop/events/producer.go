// Package events produces Kafka messages; the writer's topic comes from a
// package-level constant through the constructor, exercising softmap's
// struct-field topic resolution.
package events

import (
	"context"

	kafka "github.com/segmentio/kafka-go"

	"example.com/toyshop/model"
)

const OrderCreatedTopic = "orders.created"

type Producer struct {
	w *kafka.Writer
}

func New(brokers []string) *Producer {
	return &Producer{w: &kafka.Writer{Addr: brokers, Topic: OrderCreatedTopic}}
}

func (p *Producer) OrderCreated(ctx context.Context, o *model.Order) error {
	return p.w.WriteMessages(ctx, kafka.Message{Key: []byte(o.ID), Value: []byte(o.Item)})
}
