#!/usr/bin/env bash
set -eou pipefail

root=$1

proto_array=(
    core/crypto/pb/crypto.proto
    core/record/pb/envelope.proto
    core/peer/pb/peer_record.proto
    core/sec/insecure/pb/plaintext.proto
    p2p/host/autonat/pb/autonat.proto
    p2p/security/noise/pb/payload.proto
    p2p/transport/webrtc/pb/message.proto
    p2p/protocol/identify/pb/identify.proto
    p2p/protocol/circuitv2/pb/circuit.proto
    p2p/protocol/circuitv2/pb/voucher.proto
    p2p/protocol/autonatv2/pb/autonatv2.proto
    p2p/protocol/holepunch/pb/holepunch.proto
    p2p/host/peerstore/pstoreds/pb/pstore.proto
)

proto_paths=""
for path in "${proto_array[@]}"; do
    proto_paths+="$path "
done

protoc --proto_path=$root --go_out=$root --go_opt=paths=source_relative $proto_paths
