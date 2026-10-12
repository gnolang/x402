# The weather demo against a local node: `make demo` starts gnodev, the
# facilitator and the seller together, then `make pay` buys the weather. The
# same pieces run one per terminal as `gnodev`, `make facilitator`, `make run`.
# Every knob is ?=-overridable, so a live testnet is a handful of variables, not
# an edit; the README shows the set.
#
# The seller holds no keys. The buyer's mnemonic reaches buy.mjs through the
# environment, never a make variable, because make echoes its recipes.

SHELL := /bin/sh

GO ?= go

FACILITATOR ?= http://localhost:8402
NETWORK ?= gno:dev
RPC ?= http://127.0.0.1:26657
PAY_TO ?= g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq
PRICE ?= 250000ugnot
LISTEN ?= :8401
SELLER_URL ?= http://localhost$(LISTEN)/weather

FACILITATOR_ARGS := -rpc $(RPC) -chain-id $(subst gno:,,$(NETWORK))
SELLER_ARGS := -listen $(LISTEN) -facilitator $(FACILITATOR) -network $(NETWORK) -pay-to $(PAY_TO) -price $(PRICE)

.PHONY: all demo facilitator run pay help

all: help

demo: ## Start a local gnodev node, the facilitator and the seller; Ctrl-C stops all three
	GO='$(GO)' RPC='$(RPC)' NETWORK='$(NETWORK)' FACILITATOR='$(FACILITATOR)' SELLER_URL='$(SELLER_URL)' \
		FACILITATOR_ARGS='$(FACILITATOR_ARGS)' SELLER_ARGS='$(SELLER_ARGS)' ./demo.sh

facilitator: ## Serve a local facilitator against the node
	$(GO) run ../../cmd/gnofacilitator $(FACILITATOR_ARGS)

run: ## Serve the priced endpoint against the facilitator
	$(GO) run . $(SELLER_ARGS)

# buy.mjs imports the client mechanism by package name, so the root build has
# to have emitted it; the root Makefile owns that build.
pay: ## Pay the running seller with buy.mjs (real coins move)
	$(MAKE) -C ../.. js
	X402_GNO_RPC=$(RPC) node buy.mjs $(SELLER_URL)

help: ## List the targets
	@grep -hE '^[a-zA-Z0-9_.$$()-]+:.*## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*## "} {printf "  %-12s %s\n", $$1, $$2}'
