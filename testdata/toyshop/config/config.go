// Package config is deliberate noise: the config-reader heuristic should
// drop it from flows.
package config

type Config struct {
	DSN       string
	RedisAddr string
	Brokers   []string
}

func Load() *Config {
	return &Config{
		DSN:       "postgres://localhost/toyshop",
		RedisAddr: "localhost:6379",
		Brokers:   []string{"localhost:9092"},
	}
}

func (c *Config) Database() string { return c.DSN }
