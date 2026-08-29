package flanjredaction

// Config is the redaction processor configuration. The floor is always on and
// takes no tuning — the only knob is the optional IP rule (off by default, as no
// contract vector exercises it and it over-redacts identifiers).
type Config struct {
	// EnableIP turns on the optional IPv4/IPv6 redaction rule.
	EnableIP bool `mapstructure:"enable_ip"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error { return nil }
