package capacitor

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}

	if cfg.UserAgent != "Capacitor/1.0" {
		t.Errorf("UserAgent = %q, want %q", cfg.UserAgent, "Capacitor/1.0")
	}
	if cfg.InitialConcurrency != 100 {
		t.Errorf("InitialConcurrency = %d, want %d", cfg.InitialConcurrency, 100)
	}
	if cfg.MaxConcurrency != 100 {
		t.Errorf("MaxConcurrency = %d, want %d", cfg.MaxConcurrency, 100)
	}
	if cfg.MinConcurrency != 1 {
		t.Errorf("MinConcurrency = %d, want %d", cfg.MinConcurrency, 1)
	}
	if cfg.AcquireTimeout != 30*time.Second {
		t.Errorf("AcquireTimeout = %v, want %v", cfg.AcquireTimeout, 30*time.Second)
	}
	if cfg.StateExpiry != 30*time.Second {
		t.Errorf("StateExpiry = %v, want %v", cfg.StateExpiry, 30*time.Second)
	}
	if cfg.SignalHandlers != nil {
		t.Error("SignalHandlers should be nil")
	}
	if cfg.EnableGOAWAYHandling != false {
		t.Error("EnableGOAWAYHandling should be false")
	}
	if cfg.Transport != nil {
		t.Error("Transport should be nil")
	}
}

func TestConfig_withDefaults_NilConfig(t *testing.T) {
	var cfg *Config = nil
	result := cfg.withDefaults()

	if result == nil {
		t.Fatal("withDefaults() on nil config returned nil")
	}

	// Should return equivalent to DefaultConfig
	if result.UserAgent != "Capacitor/1.0" {
		t.Errorf("UserAgent = %q, want %q", result.UserAgent, "Capacitor/1.0")
	}
	if result.InitialConcurrency != 100 {
		t.Errorf("InitialConcurrency = %d, want %d", result.InitialConcurrency, 100)
	}
}

func TestConfig_withDefaults_ZeroValues(t *testing.T) {
	cfg := &Config{}
	result := cfg.withDefaults()

	if result.InitialConcurrency != 100 {
		t.Errorf("InitialConcurrency = %d, want %d", result.InitialConcurrency, 100)
	}
	if result.MaxConcurrency != 100 {
		t.Errorf("MaxConcurrency = %d, want %d", result.MaxConcurrency, 100)
	}
	if result.MinConcurrency != 1 {
		t.Errorf("MinConcurrency = %d, want %d", result.MinConcurrency, 1)
	}
	if result.AcquireTimeout != 30*time.Second {
		t.Errorf("AcquireTimeout = %v, want %v", result.AcquireTimeout, 30*time.Second)
	}
	if result.StateExpiry != 30*time.Second {
		t.Errorf("StateExpiry = %v, want %v", result.StateExpiry, 30*time.Second)
	}
}

func TestConfig_withDefaults_NegativeValues(t *testing.T) {
	cfg := &Config{
		InitialConcurrency: -5,
		MaxConcurrency:     -10,
		MinConcurrency:     -1,
		AcquireTimeout:     -1 * time.Second,
		StateExpiry:        -1 * time.Second,
	}
	result := cfg.withDefaults()

	if result.InitialConcurrency != 100 {
		t.Errorf("InitialConcurrency = %d, want %d", result.InitialConcurrency, 100)
	}
	if result.MaxConcurrency != 100 {
		t.Errorf("MaxConcurrency = %d, want %d", result.MaxConcurrency, 100)
	}
	if result.MinConcurrency != 1 {
		t.Errorf("MinConcurrency = %d, want %d", result.MinConcurrency, 1)
	}
	if result.AcquireTimeout != 30*time.Second {
		t.Errorf("AcquireTimeout = %v, want %v", result.AcquireTimeout, 30*time.Second)
	}
	if result.StateExpiry != 30*time.Second {
		t.Errorf("StateExpiry = %v, want %v", result.StateExpiry, 30*time.Second)
	}
}

func TestConfig_withDefaults_CustomValues(t *testing.T) {
	customTransport := &http.Transport{}
	cfg := &Config{
		UserAgent:          "Custom/2.0",
		InitialConcurrency: 50,
		MaxConcurrency:     200,
		MinConcurrency:     5,
		AcquireTimeout:     60 * time.Second,
		StateExpiry:        120 * time.Second,
		Transport:          customTransport,
	}
	result := cfg.withDefaults()

	if result.UserAgent != "Custom/2.0" {
		t.Errorf("UserAgent = %q, want %q", result.UserAgent, "Custom/2.0")
	}
	if result.InitialConcurrency != 50 {
		t.Errorf("InitialConcurrency = %d, want %d", result.InitialConcurrency, 50)
	}
	if result.MaxConcurrency != 200 {
		t.Errorf("MaxConcurrency = %d, want %d", result.MaxConcurrency, 200)
	}
	if result.MinConcurrency != 5 {
		t.Errorf("MinConcurrency = %d, want %d", result.MinConcurrency, 5)
	}
	if result.AcquireTimeout != 60*time.Second {
		t.Errorf("AcquireTimeout = %v, want %v", result.AcquireTimeout, 60*time.Second)
	}
	if result.StateExpiry != 120*time.Second {
		t.Errorf("StateExpiry = %v, want %v", result.StateExpiry, 120*time.Second)
	}
	if result.Transport != customTransport {
		t.Error("Transport should be preserved")
	}
}

func TestConfig_withDefaults_EmptyUserAgent(t *testing.T) {
	cfg := &Config{
		UserAgent:          "", // intentionally empty
		InitialConcurrency: 10,
		MaxConcurrency:     50,
		MinConcurrency:     1,
		AcquireTimeout:     5 * time.Second,
		StateExpiry:        10 * time.Second,
	}
	result := cfg.withDefaults()

	// Empty UserAgent should be preserved (not overridden)
	if result.UserAgent != "" {
		t.Errorf("UserAgent = %q, want empty string", result.UserAgent)
	}
}

func TestConfig_withDefaults_DoesNotMutateOriginal(t *testing.T) {
	cfg := &Config{
		InitialConcurrency: 0,
	}
	result := cfg.withDefaults()

	// Original should remain unchanged
	if cfg.InitialConcurrency != 0 {
		t.Error("withDefaults() mutated original config")
	}

	// Result should have default
	if result.InitialConcurrency != 100 {
		t.Errorf("result.InitialConcurrency = %d, want %d", result.InitialConcurrency, 100)
	}
}

func TestConfig_withDefaults_Callbacks(t *testing.T) {
	stateChangeCalled := false
	signalCalled := false

	cfg := &Config{
		InitialConcurrency: 10,
		MaxConcurrency:     50,
		MinConcurrency:     1,
		AcquireTimeout:     5 * time.Second,
		StateExpiry:        10 * time.Second,
		OnStateChange: func(host string, state *State) {
			stateChangeCalled = true
		},
		OnSignal: func(host string, signal *Signal) {
			signalCalled = true
		},
	}
	result := cfg.withDefaults()

	// Callbacks should be preserved
	if result.OnStateChange == nil {
		t.Error("OnStateChange callback should be preserved")
	}
	if result.OnSignal == nil {
		t.Error("OnSignal callback should be preserved")
	}

	// Verify callbacks work
	result.OnStateChange("test", nil)
	result.OnSignal("test", nil)

	if !stateChangeCalled {
		t.Error("OnStateChange was not called")
	}
	if !signalCalled {
		t.Error("OnSignal was not called")
	}
}
