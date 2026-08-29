package effects

import (
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/ssax"
)

// Kafka producer detection resolves the topic when it is a string constant,
// a package-level const/var, or a struct field set once from a constant
// (writer configured in a constructor). Anything else degrades honestly to
// Topic:"" plus a TopicExpr hint - never a silently missing effect.

func matchSegmentioProducer(info *ssax.CalleeInfo) bool {
	return info.Pkg == "github.com/segmentio/kafka-go" &&
		info.Type == "Writer" && info.Name == "WriteMessages"
}

func buildSegmentioProduce(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect {
	e := &Effect{Type: "kafka", Detail: info.Name}
	args := site.Common().Args
	// Writer.Topic set at construction is the dominant pattern.
	if len(args) > 0 {
		if topic, ok := ssax.StructFieldString(args[0], "Topic"); ok && topic != "" {
			e.Topic = topic
			return e
		}
	}
	// Per-message topics: kafka.Message{Topic: ...} in the vararg pack.
	msgArgs := args[info.ArgOffset:]
	if len(msgArgs) >= 2 {
		if topic, ok := messagesTopic(msgArgs[len(msgArgs)-1]); ok {
			e.Topic = topic
			return e
		}
	}
	e.TopicExpr = topicExpr(args)
	return e
}

func matchSaramaProducer(info *ssax.CalleeInfo) bool {
	if info.Pkg != "github.com/IBM/sarama" && info.Pkg != "github.com/Shopify/sarama" {
		return false
	}
	switch info.Type {
	case "SyncProducer":
		return info.Name == "SendMessage" || info.Name == "SendMessages"
	case "AsyncProducer":
		return info.Name == "Input"
	}
	return false
}

func buildSaramaProduce(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect {
	e := &Effect{Type: "kafka", Detail: info.Name}
	args := site.Common().Args[info.ArgOffset:]
	if info.Name == "SendMessage" && len(args) >= 1 {
		if topic, ok := ssax.StructFieldString(args[0], "Topic"); ok {
			e.Topic = topic
			return e
		}
	}
	if info.Name != "Input" {
		e.TopicExpr = topicExpr(site.Common().Args)
	}
	return e
}

func matchConfluentProducer(info *ssax.CalleeInfo) bool {
	return strings.HasPrefix(info.Pkg, "github.com/confluentinc/confluent-kafka-go") &&
		info.Type == "Producer" && info.Name == "Produce"
}

func buildConfluentProduce(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect {
	e := &Effect{Type: "kafka", Detail: info.Name}
	args := site.Common().Args[info.ArgOffset:]
	if len(args) >= 1 {
		// Message.TopicPartition.Topic is a *string; chase the address it
		// was set from.
		if topic, ok := confluentTopic(args[0]); ok {
			e.Topic = topic
			return e
		}
	}
	e.TopicExpr = topicExpr(site.Common().Args)
	return e
}

func confluentTopic(msg ssa.Value) (string, bool) {
	// Best effort: Message{TopicPartition: TopicPartition{Topic: &t}} where
	// t is stored once from a constant.
	if topic, ok := ssax.StructFieldString(msg, "Topic"); ok {
		return topic, true
	}
	return "", false
}

// messagesTopic resolves a shared Topic from the message vararg pack: all
// messages must agree on one constant topic.
func messagesTopic(pack ssa.Value) (string, bool) {
	vals := ssax.VarargValues(pack)
	if len(vals) == 0 {
		return "", false
	}
	topic := ""
	for i, v := range vals {
		t, ok := ssax.StructFieldString(v, "Topic")
		if !ok || t == "" || (i > 0 && t != topic) {
			return "", false
		}
		topic = t
	}
	return topic, true
}

// topicExpr renders a short source-level hint for an unresolved topic.
func topicExpr(args []ssa.Value) string {
	if len(args) == 0 {
		return ""
	}
	s := args[0].String()
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}
