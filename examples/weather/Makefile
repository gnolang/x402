# The weather demo against a local node: start one (`gnodev`), then `make
# facilitator`, `make run`, `make pay` — each in its own terminal. Every knob is
# ?=-overridable, so a live testnet is a handful of variables, not an edit; the
# README shows the set.
#
# The seller holds no keys. The buyer signs with MNEMONIC — a funded account on
# the chain the offer names — defaulting to gno's well-known test1, which every
# local test chain funds.

SHELL := /bin/sh

GO ?= go

FACILITATOR ?= http://localhost:8402
NETWORK ?= gno:dev
RPC ?= http://127.0.0.1:26657
PAY_TO ?= g12pdsy8maqg0x8dx7tkq2h6gyd78p9wv0d4pm6g
PRICE ?= 250000ugnot
MNEMONIC ?=
LISTEN ?= :8401
SELLER_URL ?= http://localhost$(LISTEN)/weather

.PHONY: all facilitator run pay help

all: help

facilitator: ## Serve a local facilitator against the node
	$(GO) run ../../cmd/gnofacilitator -rpc $(RPC) -chain-id $(subst gno:,,$(NETWORK))

run: ## Serve the priced endpoint against the facilitator
	$(GO) run . -listen $(LISTEN) -facilitator $(FACILITATOR) -network $(NETWORK) -pay-to $(PAY_TO) -price $(PRICE)

# buy.mjs imports the client mechanism by package name, so the root build has
# to have emitted it; the root Makefile owns that build.
pay: ## Pay the running seller with buy.mjs (real coins move)
	$(MAKE) -C ../.. js
	X402_MNEMONIC=$(MNEMONIC) X402_GNO_RPC=$(RPC) node buy.mjs $(SELLER_URL)

help: ## List the targets
	@grep -hE '^[a-zA-Z0-9_.$$()-]+:.*## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*## "} {printf "  %-8s %s\n", $$1, $$2}'
