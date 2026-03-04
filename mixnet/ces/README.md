# CES: Compress-Encrypt-Shard Pipeline

The CES package implements the core data processing pipeline for the Lib-Mix protocol. It handles data before it enters the mixnet circuits and reconstructs it at the destination.

## Pipeline Flow

1.  **Compress**: Data is compressed using the configured algorithm (gzip or snappy) to reduce the number of shards and improve performance.
2.  **Encrypt**: Data is encrypted in layers (onion encryption) using the Noise protocol. Each layer corresponds to a hop in the mixnet circuit.
3.  **Shard**: The encrypted data is split into multiple shards using Reed-Solomon erasure coding. This provides redundancy, allowing for data recovery even if some circuits fail.

## Components

- **Compressor**: Handles data compression and decompression.
- **LayeredEncrypter**: Performs multi-layer encryption for onion routing.
- **Sharder**: Implements erasure coding for multi-path transmission.
- **CESPipeline**: Coordinates the entire process.

## Security

- **Ephemeral Keys**: Encryption keys are ephemeral and per-circuit.
- **Memory Security**: Keys are explicitly erased from memory after use.
- **No Metadata**: Padding is applied to ensure shards are of uniform size, preventing traffic analysis based on message length.
