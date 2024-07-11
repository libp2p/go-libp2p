Tests that leak goroutines for various reasons. Mostly because libp2p node shutdown logic doesn't run if we fail to construct the node.
