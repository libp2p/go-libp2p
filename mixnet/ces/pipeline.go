package ces

import (
	"fmt"
)

// Config holds CES pipeline configuration
type Config struct {
	HopCount        int
	CircuitCount    int
	Compression     string
	ErasureThreshold int
}

// CESPipeline coordinates Compress-Encrypt-Shard operations
type CESPipeline struct {
	cfg        *Config
	compressor Compressor
	sharder    *Sharder
	encrypter  *LayeredEncrypter
}

// NewPipeline creates a new CES pipeline with the given configuration
func NewPipeline(cfg *Config) *CESPipeline {
	threshold := cfg.ErasureThreshold
	if threshold == 0 {
		// ceil(N * 0.6) per design document "Data Models"
		threshold = (cfg.CircuitCount*6 + 9) / 10
		if threshold < 1 {
			threshold = 1
		}
	}

	return &CESPipeline{
		cfg:        cfg,
		compressor: NewCompressor(cfg.Compression),
		sharder:    NewSharder(cfg.CircuitCount, threshold),
		encrypter:  NewLayeredEncrypter(cfg.HopCount),
	}
}

// Process applies the full CES pipeline: Compress -> Encrypt -> Shard
// Destinations should be ordered from entry to exit relay
func (p *CESPipeline) Process(data []byte, destinations []string) ([]*Shard, error) {
	shards, _, err := p.ProcessWithKeys(data, destinations)
	return shards, err
}

// ProcessWithKeys applies the full CES pipeline and returns shards with encryption keys
// Destinations should be ordered from entry to exit relay
func (p *CESPipeline) ProcessWithKeys(data []byte, destinations []string) ([]*Shard, []*EncryptionKey, error) {
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("empty data")
	}

	if len(destinations) != p.cfg.HopCount {
		return nil, nil, fmt.Errorf("expected %d destinations, got %d", p.cfg.HopCount, len(destinations))
	}

	// Step 1: Compress (Req 3.1)
	compressed, err := p.compressor.Compress(data)
	if err != nil {
		return nil, nil, fmt.Errorf("compression failed: %w", err)
	}

	// Step 2: Encrypt (layered onion encryption) (Req 3.3, 16)
	encrypted, keys, err := p.encrypter.Encrypt(compressed, destinations)
	if err != nil {
		return nil, nil, fmt.Errorf("encryption failed: %w", err)
	}

	// Step 3: Shard (erasure coding) (Req 3.4-3.5)
	shards, err := p.sharder.Shard(encrypted)
	if err != nil {
		return nil, nil, fmt.Errorf("sharding failed: %w", err)
	}

	return shards, keys, nil
}

// Reconstruct applies inverse: Reconstruct -> Decrypt -> Decompress
// Note: Keys must be provided in the same order as they were generated
func (p *CESPipeline) Reconstruct(shards []*Shard, keys []*EncryptionKey) ([]byte, error) {
	threshold := p.cfg.ErasureThreshold
	if threshold == 0 {
		// ceil(N * 0.6) per design document "Data Models"
		threshold = (p.cfg.CircuitCount*6 + 9) / 10
		if threshold < 1 {
			threshold = 1
		}
	}

	if len(shards) < threshold {
		return nil, fmt.Errorf("insufficient shards: have %d, need %d",
			len(shards), threshold)
	}

	if len(keys) != p.cfg.HopCount {
		return nil, fmt.Errorf("expected %d keys, got %d", p.cfg.HopCount, len(keys))
	}

	// Step 1: Reconstruct encrypted data from shards
	encrypted, err := p.sharder.Reconstruct(shards)
	if err != nil {
		return nil, fmt.Errorf("reconstruction failed: %w", err)
	}

	// Step 2: Decrypt (layered onion decryption)
	decrypted, err := p.encrypter.Decrypt(encrypted, keys)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	// Step 3: Decompress
	decompressed, err := p.compressor.Decompress(decrypted)
	if err != nil {
		return nil, fmt.Errorf("decompression failed: %w", err)
	}

	return decompressed, nil
}

// Config returns the pipeline configuration
func (p *CESPipeline) Config() *Config {
	return p.cfg
}

// Compressor returns the underlying compressor
func (p *CESPipeline) Compressor() Compressor {
	return p.compressor
}

// Sharder returns the underlying sharder
func (p *CESPipeline) Sharder() *Sharder {
	return p.sharder
}

// Encrypter returns the underlying encrypter
func (p *CESPipeline) Encrypter() *LayeredEncrypter {
	return p.encrypter
}
