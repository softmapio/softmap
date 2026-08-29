// Package kafka is a minimal stub of github.com/segmentio/kafka-go carrying
// only the signatures softmap's effect detectors and entrypoint matchers key
// on (internal/effects, internal/entrypoints).
package kafka

import "context"

type Message struct {
	Topic string
	Key   []byte
	Value []byte
}

type Writer struct {
	Addr  any
	Topic string
}

func (w *Writer) WriteMessages(ctx context.Context, msgs ...Message) error { return nil }
func (w *Writer) Close() error                                             { return nil }

type ReaderConfig struct {
	Brokers []string
	GroupID string
	Topic   string
}

type Reader struct{}

func NewReader(config ReaderConfig) *Reader { return &Reader{} }

func (r *Reader) ReadMessage(ctx context.Context) (Message, error)          { return Message{}, nil }
func (r *Reader) FetchMessage(ctx context.Context) (Message, error)         { return Message{}, nil }
func (r *Reader) CommitMessages(ctx context.Context, msgs ...Message) error { return nil }
func (r *Reader) Close() error                                              { return nil }
