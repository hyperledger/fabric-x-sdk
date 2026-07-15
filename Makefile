.PHONY: checks
checks:
	@test -z $(shell gofmt -l -s $(shell go list -f '{{.Dir}}' ./...) | tee /dev/stderr) || (echo "Fix formatting issues"; exit 1)
	@go vet -all $(shell go list -f '{{.Dir}}' ./...)
	find . -type d -name testdata -prune -o -name '*.go' -print | xargs go tool addlicense -check || (echo "Missing license headers"; exit 1)

.PHONY: unit-tests
unit-tests:
	go test ./... -short

# The committer test node is self-contained: it ships its own crypto material
# (peer-org-0 / orderer-org-0), genesis/config block and per-service configs,
# all wired together with mTLS. We use those embedded configs as-is and only
# override a few keys via environment variables.
COMMITTER_IMAGE ?= docker.io/hyperledger/fabric-x-committer-test-node:1.0.4
# fxconfig (namespace administration) ships in the fabric-x tools image.
TOOLS_IMAGE ?= docker.io/hyperledger/fabric-x-tools:1.0.1

# init-x extracts the crypto material embedded in the committer image so the
# integration test and fxconfig can use the same identities the committer trusts.
# The crypto is extracted with the image's restrictive modes (0750 dirs, 0600
# files); on Linux that blocks the fxconfig tools container (a different uid)
# from reading it over the bind mount, so we make this test material readable.
.PHONY: init-x
init-x:
	@rm -rf testdata/crypto && mkdir -p testdata/crypto
	@cid=$$(docker create $(COMMITTER_IMAGE)); \
		docker cp $$cid:/root/artifacts/. testdata/crypto; \
		docker rm $$cid >/dev/null
	@chmod -R a+rX testdata/crypto

# clean-x removes the crypto. Recreate with init-x.
.PHONY: clean-x
clean-x:
	@rm -rf testdata/crypto

# start-x runs the whole committer pipeline (embedded DB, mock orderer and the
# five committer microservices) in a single container.
.PHONY: start-x
start-x:
	@docker run -d --rm --name fabric-x-committer-test-node \
		--add-host coordinator:127.0.0.1 --add-host verifier:127.0.0.1 \
		--add-host vc:127.0.0.1 --add-host db:127.0.0.1 \
		-e SC_ORDERER_BLOCK_SIZE=1 \
		-p 4001:4001 -p 7001:7001 -p 7050:7050 \
		$(COMMITTER_IMAGE) run db orderer committer
	@echo "Waiting for the committer to become healthy..."
	@while ! docker exec fabric-x-committer-test-node /root/bin/healthcheck >/dev/null 2>&1; do sleep 1; done
	@echo "Creating namespace 'basic'..."
	@docker run --rm --network container:fabric-x-committer-test-node \
		-v "$(PWD)/testdata/crypto:/crypto:ro" \
		-v "$(PWD)/testdata/fxconfig.yaml:/config/fxconfig.yaml:ro" \
		$(TOOLS_IMAGE) \
		sh -c "fxconfig namespace list --config=/config/fxconfig.yaml 2>/dev/null | grep -q 'basic' || ( \
			fxconfig namespace create basic --policy=\"OR('peer-org-0.member', 'peer-org-1.member')\" --output /tmp/tx.json --config=/config/fxconfig.yaml && \
			fxconfig tx endorse /tmp/tx.json --output /tmp/tx.org0.json --config=/config/fxconfig.yaml && \
			FXCONFIG_MSP_LOCALMSPID=peer-org-1 FXCONFIG_MSP_CONFIGPATH=/crypto/peerOrganizations/peer-org-1.com/users/client@peer-org-1.com/msp \
				fxconfig tx endorse /tmp/tx.org0.json --output /tmp/tx.org01.json --config=/config/fxconfig.yaml && \
			fxconfig tx submit /tmp/tx.org01.json --wait --config=/config/fxconfig.yaml )"

.PHONY: test-x
test-x:
	@go test -timeout 30s -run ^TestFabricXCommitter$$ -count=1 ./integration

.PHONY: stop-x
stop-x:
	@docker rm -f fabric-x-committer-test-node

.PHONY: start-fablo
start-fablo:
	docker pull hyperledger/fabric-ccenv:3.1.4; docker pull hyperledger/fabric-baseos:3.1.4
	cd testdata/fablo && ./fablo up

.PHONY: stop-fablo
stop-fablo:
	cd testdata/fablo && ./fablo prune

.PHONY: test-fablo
test-fablo:
	@go test -timeout 60s -run ^TestFablo$$ -count=1 ./integration

.PHONY: clean-fablo
clean-fablo:
	cd testdata/fablo && ./fablo prune || true
	rm -rf testdata/fablo/snapshot.fablo.tar.gz
