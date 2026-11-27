Talos filters traffic to the router's addresses before routald's DNAT runs: `routald talos-patch`
also writes a Talos `NetworkRuleConfig` for each forward, so that the packets reach it.
