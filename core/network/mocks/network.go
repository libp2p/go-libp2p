package mocknetwork

//go:generate mockgen -package mocknetwork -destination mock_resource_manager.go github.com/libp2p/go-libp2p/core/network ResourceManager
//go:generate mockgen -package mocknetwork -destination mock_conn_management_scope.go github.com/libp2p/go-libp2p/core/network ConnManagementScope
//go:generate mockgen -package mocknetwork -destination mock_stream_management_scope.go github.com/libp2p/go-libp2p/core/network StreamManagementScope
//go:generate mockgen -package mocknetwork -destination mock_peer_scope.go github.com/libp2p/go-libp2p/core/network PeerScope
//go:generate mockgen -package mocknetwork -destination mock_protocol_scope.go github.com/libp2p/go-libp2p/core/network ProtocolScope
//go:generate mockgen -package mocknetwork -destination mock_resource_scope_span.go github.com/libp2p/go-libp2p/core/network ResourceScopeSpan
